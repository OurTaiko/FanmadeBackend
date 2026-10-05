package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DockerDumper struct{ Container, User string }

func (d DockerDumper) Dump(ctx context.Context, database, path string) (string, error) {
	if d.Container == "" || d.User == "" {
		return "", errors.New("PostgreSQL container and user required")
	}
	// Reuse the exact running image: pg_dump and pg_restore always match the server.
	out, err := exec.CommandContext(ctx, "docker", "inspect", d.Container, "--format", "{{.Image}}").Output()
	if err != nil {
		return "", errors.New("inspect PostgreSQL container failed")
	}
	image := strings.TrimSpace(string(out))
	if !strings.HasPrefix(image, "sha256:") {
		return "", errors.New("invalid PostgreSQL image identity")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "docker", "exec", d.Container, "pg_dump", "--username="+d.User, "--dbname="+database, "--format=custom", "--compress=gzip:6", "--lock-wait-timeout=60s", "--no-password")
	cmd.Stdout = f
	// Do not log SQL, connection strings or child output; status identifies the failed step.
	if err = cmd.Run(); err != nil {
		f.Close()
		return "", fmt.Errorf("pg_dump failed: %s", safeExit(err))
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return image, nil
}
func (d DockerDumper) Validate(ctx context.Context, dir, image string) error {
	// --file reads/decompresses the complete archive, unlike --list (TOC only).
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--mount", "type=bind,src="+dir+",dst=/backup,readonly", image, "pg_restore", "--file=/dev/null", filepath.Join("/backup", DumpName))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("archive validation failed: %s", safeExit(err))
	}
	return nil
}
func safeExit(err error) string {
	var e *exec.ExitError
	if errors.As(err, &e) {
		return fmt.Sprintf("exit %d", e.ExitCode())
	}
	return "command unavailable or interrupted"
}
