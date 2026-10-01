// Command gosources writes the Flatpak manifest's Go sources: every module
// file the tray app's build reads, as a download from proxy.golang.org with
// its SHA-256, laid out as a GOPROXY=file:// directory. flatpak-builder
// fetches them before the build, so the build itself needs no network —
// which Flathub requires — and go.sum still verifies every module.
//
//	go run ./app/build/linux/flatpak/gosources > app/build/linux/flatpak/go-sources.json
//
// Zips are listed only for modules that provide packages to ./app; the
// module graph's .mod files are all listed, since resolving the build list
// reads them. .info files are not listed: the build does not need them, and
// unlike .mod and .zip they are not covered by go.sum, so a local cache can
// hold a copy that differs from the proxy's (seen: atomicgo.dev/cursor) —
// which flatpak-builder then rejects on its checksum. With -toolchain, the Go toolchain module is added too, for an
// SDK whose Go is older than go.mod asks for.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode"
)

type source struct {
	Type         string `json:"type"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Dest         string `json:"dest"`
	DestFilename string `json:"dest-filename"`
}

type download struct {
	Path, Version, Info, GoMod, Zip, Error string
}

const proxy = "https://proxy.golang.org"

func main() {
	toolchain := flag.String("toolchain", "", "also vendor this Go toolchain, e.g. go1.27.1")
	flag.Parse()

	needZip := map[string]bool{}
	for _, m := range lines("go", "list", "-deps", "-tags", "production",
		"-f", "{{if .Module}}{{if .Module.Version}}{{.Module.Path}}@{{.Module.Version}}{{end}}{{end}}", "./app") {
		needZip[m] = true
	}
	all := lines("go", "list", "-m", "-f", "{{if .Version}}{{.Path}}@{{.Version}}{{end}}", "all")
	if *toolchain != "" {
		m := "golang.org/toolchain@v0.0.1-" + *toolchain + ".linux-amd64"
		all = append(all, m)
		needZip[m] = true
	}
	sort.Strings(all)

	var out []source
	for _, m := range all {
		d := fetch(m)
		files := []struct{ local, ext string }{{d.GoMod, ".mod"}}
		if needZip[m] {
			files = append(files, struct{ local, ext string }{d.Zip, ".zip"})
		}
		dir := "goproxy/" + escape(d.Path) + "/@v"
		for _, f := range files {
			name := escape(d.Version) + f.ext
			out = append(out, source{
				Type: "file", URL: proxy + "/" + escape(d.Path) + "/@v/" + name,
				SHA256: sha(f.local), Dest: dir, DestFilename: name,
			})
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "%d modules, %d with sources, %d files\n", len(all), len(needZip), len(out))
}

// fetch makes sure a module's files are in the local cache and says where.
func fetch(m string) download {
	out, err := exec.Command("go", "mod", "download", "-json", m).Output()
	var d download
	if jerr := json.Unmarshal(out, &d); jerr != nil || d.Error != "" {
		fail(fmt.Errorf("go mod download %s: %v %s %v", m, err, d.Error, jerr))
	}
	return d
}

// escape is the module proxy's case encoding: an uppercase letter becomes
// '!' and the letter in lower case, so paths survive case-folding disks.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sha(path string) string {
	f, err := os.Open(path)
	if err != nil {
		fail(err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		fail(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func lines(name string, args ...string) []string {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fail(fmt.Errorf("%s %v: %w", name, args, err))
	}
	var r []string
	seen := map[string]bool{}
	for l := range strings.Lines(string(out)) {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			r = append(r, l)
		}
	}
	return r
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gosources:", err)
	os.Exit(1)
}
