package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// handlerReturning 返回一个固定响应状态码的处理器。
func handlerReturning(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})
}

func doLogin(h http.Handler, remoteAddr string) int {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestLoginRateLimiter_LocksAfterThreshold(t *testing.T) {
	l := NewLoginRateLimiter()
	h := l.Middleware(handlerReturning(http.StatusUnauthorized))
	const ip = "203.0.113.7:12345"

	// 前 failThreshold 次应穿透到下游（返回 401），不被限流
	for i := 0; i < failThreshold; i++ {
		if code := doLogin(h, ip); code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次应为 401，实际 %d", i+1, code)
		}
	}

	// 越过阈值后应被封禁，返回 429 且带 Retry-After
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = ip
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("阈值后应为 429，实际 %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 响应应带 Retry-After 头")
	}
}

func TestLoginRateLimiter_LoopbackExempt(t *testing.T) {
	l := NewLoginRateLimiter()
	h := l.Middleware(handlerReturning(http.StatusUnauthorized))

	// 回环地址无论失败多少次都不应被限流
	for i := 0; i < failThreshold*3; i++ {
		if code := doLogin(h, "127.0.0.1:5555"); code != http.StatusUnauthorized {
			t.Fatalf("回环第 %d 次应始终为 401，实际 %d", i+1, code)
		}
	}
}

func TestLoginRateLimiter_SuccessResets(t *testing.T) {
	l := NewLoginRateLimiter()
	const ip = "203.0.113.8:4444"

	failH := l.Middleware(handlerReturning(http.StatusUnauthorized))
	okH := l.Middleware(handlerReturning(http.StatusOK))

	// 失败若干次（未到阈值），然后成功一次应清零计数
	for i := 0; i < failThreshold-1; i++ {
		doLogin(failH, ip)
	}
	if code := doLogin(okH, ip); code != http.StatusOK {
		t.Fatalf("成功登录应为 200，实际 %d", code)
	}

	// 清零后应能重新从头累计 failThreshold 次而不被提前封禁
	for i := 0; i < failThreshold; i++ {
		if code := doLogin(failH, ip); code != http.StatusUnauthorized {
			t.Fatalf("清零后第 %d 次应为 401，实际 %d", i+1, code)
		}
	}
}
