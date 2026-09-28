// Command check runs the repository verification stages.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	staticcheckPkg = "honnef.co/go/tools/cmd/staticcheck@v0.8.1"
	govulncheckPkg = "golang.org/x/vuln/cmd/govulncheck@v1.8.0"
)

type result struct {
	Name   string
	Status string // PASS, FAIL, SKIPPED
	Reason string
}

func main() {
	quick := flag.Bool("quick", false, "run vet, staticcheck, and tests only")
	flag.Parse()
	root, err := moduleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Chdir(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ci := os.Getenv("CI") == "true"
	var results []result
	results = append(results, runCmd("vet", "go", "vet", "./..."))
	results = append(results, runCmd("staticcheck", "go", "run", staticcheckPkg, "./..."))
	if *quick {
		results = append(results, runCmd("test", "go", "test", "./..."))
		for _, name := range []string{"govulncheck", "race", "fuzz", "perf", "e2e", "container"} {
			results = append(results, result{Name: name, Status: "SKIPPED", Reason: "-quick"})
		}
	} else {
		results = append(results, runCmd("govulncheck", "go", "run", govulncheckPkg, "./..."))
		if cgoReady() {
			results = append(results, runCmd("test", "go", "test", "-race", "./..."))
			results = append(results, result{Name: "race", Status: "PASS"})
		} else {
			results = append(results, runCmd("test", "go", "test", "./..."))
			results = append(results, skipOrFail("race", "no cgo", ci))
		}
		results = append(results, fuzzStage(ci))
		results = append(results, runCmd("perf", "go", "test", "-tags", "perf", "./..."))
		results = append(results, e2eStage(ci))
		results = append(results, containerStage(ci))
	}
	fail := printTable(results)
	if fail {
		os.Exit(1)
	}
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("go list -m: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func runCmd(name, bin string, args ...string) result {
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "--- %s ---\n%s\n", name, buf.String())
		return result{Name: name, Status: "FAIL", Reason: err.Error()}
	}
	return result{Name: name, Status: "PASS"}
}

func skipOrFail(name, reason string, ci bool) result {
	if ci {
		return result{Name: name, Status: "FAIL", Reason: reason + " (CI forbids skip)"}
	}
	fmt.Printf("%s: SKIPPED (%s)\n", name, reason)
	return result{Name: name, Status: "SKIPPED", Reason: reason}
}

func cgoReady() bool {
	out, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		return false
	}
	for _, cc := range []string{"gcc", "clang", "cc", "cl"} {
		if _, err := exec.LookPath(cc); err == nil {
			return true
		}
	}
	return false
}

var fuzzRe = regexp.MustCompile(`func (Fuzz[A-Za-z0-9_]+)\(`)

func fuzzStage(ci bool) result {
	targets, err := findFuzz(".")
	if err != nil {
		return result{Name: "fuzz", Status: "FAIL", Reason: err.Error()}
	}
	if len(targets) == 0 {
		return result{Name: "fuzz", Status: "PASS", Reason: "no targets"}
	}
	for _, tg := range targets {
		r := runCmd("fuzz "+tg.name, "go", "test", "-run=^$", "-fuzz=^"+tg.name+"$", "-fuzztime=15s", tg.pkg)
		if r.Status != "PASS" {
			r.Name = "fuzz"
			return r
		}
	}
	_ = ci
	return result{Name: "fuzz", Status: "PASS"}
}

type fuzzTarget struct {
	pkg  string
	name string
}

func findFuzz(root string) ([]fuzzTarget, error) {
	var out []fuzzTarget
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == "vendor" || base == "testdata" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		if dir == "." {
			dir = "."
		}
		pkg := "./" + dir
		if dir == "." {
			pkg = "."
		}
		for _, m := range fuzzRe.FindAllSubmatch(b, -1) {
			out = append(out, fuzzTarget{pkg: pkg, name: string(m[1])})
		}
		return nil
	})
	return out, err
}

func e2eStage(ci bool) result {
	if !chromeFound() {
		return skipOrFail("e2e", "no Chrome or Chromium", ci)
	}
	return runCmd("e2e", "go", "test", "-tags", "e2e", "./...")
}

func chromeFound() bool {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return true
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	if runtime.GOOS == "windows" {
		candidates := []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), `Google\Chrome\Application\chrome.exe`),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), `Google\Chrome\Application\chrome.exe`),
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return true
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if st, err := os.Stat("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

func containerStage(ci bool) result {
	if _, err := exec.LookPath("docker"); err != nil {
		return skipOrFail("container", "docker unavailable", ci)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	name := "journal-check-smoke"
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	build := exec.CommandContext(ctx, "docker", "build", "-t", "journal-check", ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "--- container build ---\n%s\n", out)
		return result{Name: "container", Status: "FAIL", Reason: "docker build: " + err.Error()}
	}
	secret := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	run := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name,
		"--memory", "512m", "--cpus", "1",
		"-p", "127.0.0.1::8080",
		"-e", "SECRET_KEY="+secret,
		"-e", "OPERATOR_CONTACT=operator@example.com",
		"-e", "BASE_URL=http://127.0.0.1:8080",
		"-e", "DB_PATH=/data/journal.db",
		"-e", "DATA_DIR=/data",
		"-e", "LISTEN_ADDR=:8080",
		"journal-check")
	out, err := run.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "--- container run ---\n%s\n", out)
		return result{Name: "container", Status: "FAIL", Reason: "docker run: " + err.Error()}
	}
	defer exec.Command("docker", "rm", "-f", name).Run()
	portOut, err := exec.CommandContext(ctx, "docker", "port", name, "8080/tcp").CombinedOutput()
	if err != nil {
		return result{Name: "container", Status: "FAIL", Reason: "docker port: " + string(portOut)}
	}
	hostPort := strings.TrimSpace(string(portOut))
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		hostPort = hostPort[i+1:]
	}
	base := "http://127.0.0.1:" + hostPort
	deadline := time.Now().Add(60 * time.Second)
	var healthErr error
	for time.Now().Before(deadline) {
		healthErr = nil
		for _, p := range []string{"/healthz", "/readyz"} {
			c := exec.CommandContext(ctx, "docker", "exec", name, "wget", "-qO-", "http://127.0.0.1:8080"+p)
			if b, err := c.CombinedOutput(); err != nil {
				healthErr = fmt.Errorf("%s: %v %s", p, err, b)
				break
			}
		}
		if healthErr == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if healthErr != nil {
		logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
		fmt.Fprintf(os.Stderr, "--- container logs ---\n%s\n", logs)
		return result{Name: "container", Status: "FAIL", Reason: healthErr.Error()}
	}
	_ = base
	inv := exec.CommandContext(ctx, "docker", "exec", name, "journal", "invite", "--admin")
	invOut, err := inv.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "--- invite ---\n%s\n", invOut)
		return result{Name: "container", Status: "FAIL", Reason: "invite: " + err.Error()}
	}
	if !strings.Contains(string(invOut), "http") {
		return result{Name: "container", Status: "FAIL", Reason: "invite did not print a URL"}
	}
	return result{Name: "container", Status: "PASS"}
}

func printTable(results []result) bool {
	w := 12
	for _, r := range results {
		if len(r.Name) > w {
			w = len(r.Name)
		}
	}
	fail := false
	fmt.Println()
	for _, r := range results {
		reason := ""
		if r.Reason != "" {
			reason = "  " + r.Reason
		}
		fmt.Printf("%-*s  %s%s\n", w, r.Name, r.Status, reason)
		if r.Status == "FAIL" {
			fail = true
		}
	}
	return fail
}
