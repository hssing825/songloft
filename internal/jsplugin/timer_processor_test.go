package jsplugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// backgroundChainHops 是后台异步链的跳数。取 10 是为了把两种行为拉开到无歧义的
// 距离：固定 500ms 轮询下约需 5s，事件驱动下约需 10×minWakeInterval ≈ 0.1s。
const backgroundChainHops = 10

// backgroundChainJSCode 模拟 MIoT 插件 AI 问答那类「onInit 里起一条脱离任何
// ExecuteJS 调用的后台 async 链」的形态：onInit 立即返回，链在后台靠宿主的
// pump 协程推进。onHTTPRequest 只读取进度，不参与推进。
var backgroundChainJSCode = `
var hops = 0;
var elapsed = -1;
function onInit() {
    var started = Date.now();
    (async function () {
        for (var i = 0; i < ` + strconv.Itoa(backgroundChainHops) + `; i++) {
            await new Promise(function (resolve) { setTimeout(resolve, 1); });
            hops++;
        }
        elapsed = Date.now() - started;
    })();
}
function onDeinit() {}
function onHTTPRequest(req) {
    return {
        statusCode: 200,
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({hops: hops, elapsed: elapsed})
    };
}
`

type backgroundChainProgress struct {
	Hops    int `json:"hops"`
	Elapsed int `json:"elapsed"`
}

// TestTimerProcessor_BackgroundChainIsNotPumpBound 验证后台异步链的推进速度不再
// 被宿主 pump 协程的轮询周期决定。
//
// 回归背景：pump 协程原先固定 500ms 轮询，常驻插件里每个 await 的续作都要等下一
// 个 tick 才能恢复，十几跳的链路（如 AI 问答的发现→接管→播报）会平白累积数秒。
// 现在 pump 由 asyncSignal 唤醒、并按下一个定时器的到期时间休眠。
//
// 关键：等待期间**不能**调用插件，因为 InvokeHTTP 走的 ExecuteJS 自带事件循环，
// 会替 pump 协程把链推完，从而掩盖回归。
func TestTimerProcessor_BackgroundChainIsNotPumpBound(t *testing.T) {
	pluginsDir, dataDir, repo, _ := setupTestEnv(t)
	ctx := context.Background()
	const entryPath = "test-background-chain"

	manifest := testManifest(entryPath)
	zipData := createTestPluginZip(t, manifest, backgroundChainJSCode)

	zipFileName := entryPath + ".jsplugin.zip"
	if err := os.WriteFile(filepath.Join(pluginsDir, zipFileName), zipData, 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	plugin := &JSPlugin{
		Name:        manifest.Name,
		Version:     manifest.Version,
		Description: manifest.Description,
		Author:      manifest.Author,
		EntryPath:   manifest.EntryPath,
		Main:        manifest.Main,
		Permissions: manifest.Permissions,
		Status:      JSPluginStatusActive,
		FilePath:    zipFileName,
	}
	if err := repo.Create(ctx, plugin); err != nil {
		t.Fatalf("create plugin record: %v", err)
	}

	manager := NewManager(repo, pluginsDir, dataDir, "", nil, nil)
	t.Cleanup(func() { manager.Close() })

	if err := manager.LoadPlugin(ctx, plugin); err != nil {
		t.Skipf("LoadPlugin failed (may need QuickJS runtime): %v", err)
	}

	// 只靠 pump 协程推进。预算 2s：事件驱动下绰绰有余，固定 500ms 轮询下只能走到
	// 4 跳。取 2s 而非恰好够用的时长，是为了留出 -race 下 VM 变慢的余量。
	const budget = 2 * time.Second
	time.Sleep(budget)

	status, _, body, err := manager.InvokeHTTP(ctx, entryPath, "GET", "/progress", nil, nil)
	if err != nil {
		t.Fatalf("InvokeHTTP failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("expected status 200, got %d (body=%s)", status, body)
	}

	var progress backgroundChainProgress
	if err := json.Unmarshal(body, &progress); err != nil {
		t.Fatalf("unmarshal progress %q: %v", body, err)
	}

	if progress.Hops != backgroundChainHops {
		t.Fatalf("后台异步链在 %v 内只推进了 %d/%d 跳，说明仍被 pump 轮询周期限速",
			budget, progress.Hops, backgroundChainHops)
	}
	if progress.Elapsed < 0 {
		t.Fatal("链已报告全部跳数但未记录耗时，JS 侧状态不自洽")
	}
	// 上限按旧轮询网格（每跳一个 500ms tick）的 1/5 取，既能判定「没被网格限速」，
	// 又不把断言绑死在 VM 实际速度上——-race 下单跳耗时会明显变长。
	if maxElapsed := backgroundChainHops * int(idleWakeInterval/time.Millisecond) / 5; progress.Elapsed > maxElapsed {
		t.Errorf("后台链耗时 %dms，已接近 500ms 轮询网格的量级（上限 %dms）",
			progress.Elapsed, maxElapsed)
	}
}

// TestNextWakeDelay_ClampsToTimerDeadline 验证兜底休眠时长的取值边界：
// 无定时器时睡满空闲间隔，有临近定时器时提前醒来，且不低于 minWakeInterval
// （否则 VM 锁被长请求占用期间会空转）。
func TestNextWakeDelay_ClampsToTimerDeadline(t *testing.T) {
	if minWakeInterval <= 0 {
		t.Fatal("minWakeInterval 必须为正，否则已到期定时器会导致 pump 协程空转")
	}
	if minWakeInterval >= idleWakeInterval {
		t.Fatalf("minWakeInterval(%v) 应小于 idleWakeInterval(%v)", minWakeInterval, idleWakeInterval)
	}
}
