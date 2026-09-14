package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The mailbox exists only in tests. Production has no code-reading or bypass endpoint.
type browserMailbox struct {
	mu    sync.Mutex
	path  string
	codes map[string]string
}

func (m *browserMailbox) SendRegistration(_ context.Context, email, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[email] = code
	data, err := json.Marshal(m.codes)
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}
func TestRegistrationBrowser(t *testing.T) {
	if os.Getenv("FRONTEND_E2E") != "1" {
		t.Skip("set FRONTEND_E2E=1 to run registration browser integration")
	}
	pool := scoreTestDB(t)
	dir := t.TempDir()
	box := &browserMailbox{path: filepath.Join(dir, "mailbox.json"), codes: map[string]string{}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)
	server := httptest.NewServer(New(pool, Config{Origin: origin, Mailer: box, Storage: dir}).Handler())
	defer server.Close()
	front, err := filepath.Abs("../../../frontend")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	logFile, err := os.Create(filepath.Join(dir, "vite.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	dev := exec.CommandContext(ctx, "pnpm", "exec", "vite", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--strictPort")
	dev.Dir = front
	dev.Env = append(os.Environ(), "VITE_API_TARGET="+server.URL)
	dev.Stdout = logFile
	dev.Stderr = logFile
	if err = dev.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = dev.Wait() }()
	client := http.Client{Timeout: time.Second}
	ready := false
	for n := 0; n < 100; n++ {
		response, e := client.Get(origin)
		if e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		data, _ := os.ReadFile(logFile.Name())
		t.Fatalf("Vite did not start: %s", data)
	}
	cmd := exec.CommandContext(ctx, "pnpm", "exec", "playwright", "test", "registration.spec.ts")
	cmd.Dir = front
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_BASE_URL="+origin, "FANMADE_TEST_MAILBOX="+box.path)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	var verified int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email_verified_at IS NOT NULL`).Scan(&verified); err != nil || verified != 1 {
		t.Fatal("expected exactly one verified browser registration", verified, err)
	}
}
