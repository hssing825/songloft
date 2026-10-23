package middleware

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// 登录限流参数：连续失败达到 failThreshold 次后开始按指数退避封禁来源 IP。
// 退避时长 = backoffBase << (fails-failThreshold)，上限 backoffMax。
const (
	failThreshold = 10
	backoffBase   = 5 * time.Second
	backoffMax    = 15 * time.Minute
	entryTTL      = time.Hour        // 超过此时长未再出现的 IP 记录清理掉
	sweepInterval = 10 * time.Minute // 懒清理间隔，避免 map 无限增长
)

type loginAttempt struct {
	fails       int
	lockedUntil time.Time
	last        time.Time
}

// LoginRateLimiter 是针对登录接口的纯内存、按源 IP 的爆破限流器。
// 单进程单管理员场景够用，不依赖 Redis。回环地址豁免（App 本地自动登录走回环）。
type LoginRateLimiter struct {
	mu        sync.Mutex
	attempts  map[string]*loginAttempt
	lastSweep time.Time
}

func NewLoginRateLimiter() *LoginRateLimiter {
	return &LoginRateLimiter{
		attempts:  make(map[string]*loginAttempt),
		lastSweep: time.Now(),
	}
}

// Middleware 包裹登录处理器：先判断来源 IP 是否处于封禁窗口，放行后按响应状态码
// 记账——401（凭据错误）计一次失败，200（成功）清零，其余状态忽略。
func (l *LoginRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)

		if !isLoopbackIP(ip) {
			if retry, locked := l.locked(ip); locked {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				respondAuthError(w, http.StatusTooManyRequests, "登录尝试过于频繁，请稍后再试", nil)
				return
			}
		}

		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		if !isLoopbackIP(ip) {
			switch sw.status {
			case http.StatusOK:
				l.record(ip, true)
			case http.StatusUnauthorized:
				l.record(ip, false)
			}
		}
	})
}

// locked 报告该 IP 当前是否处于封禁窗口，以及剩余时长。
func (l *LoginRateLimiter) locked(ip string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked()

	a := l.attempts[ip]
	if a == nil {
		return 0, false
	}
	if remaining := time.Until(a.lockedUntil); remaining > 0 {
		return remaining, true
	}
	return 0, false
}

// record 更新某 IP 的失败计数：成功则清零，失败累加并在越过阈值后设置退避封禁。
func (l *LoginRateLimiter) record(ip string, success bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if success {
		delete(l.attempts, ip)
		return
	}

	a := l.attempts[ip]
	if a == nil {
		a = &loginAttempt{}
		l.attempts[ip] = a
	}
	a.fails++
	a.last = time.Now()
	if a.fails >= failThreshold {
		a.lockedUntil = a.last.Add(backoff(a.fails))
	}
}

// sweepLocked 懒清理过期记录。调用方需已持锁。
func (l *LoginRateLimiter) sweepLocked() {
	now := time.Now()
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	for ip, a := range l.attempts {
		if now.Sub(a.last) > entryTTL && now.After(a.lockedUntil) {
			delete(l.attempts, ip)
		}
	}
}

func backoff(fails int) time.Duration {
	shift := fails - failThreshold
	if shift < 0 {
		shift = 0
	}
	// 防止移位溢出：超过阈值一定次数后直接取上限。
	if shift > 20 {
		return backoffMax
	}
	d := backoffBase << uint(shift)
	if d > backoffMax || d <= 0 {
		return backoffMax
	}
	return d
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// statusRecorder 捕获下游写入的状态码，用于限流记账。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}
