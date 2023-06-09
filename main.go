package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	emoji "github.com/yuin/goldmark-emoji"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"go.abhg.dev/goldmark/mermaid"
	"go.abhg.dev/goldmark/wikilink"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

var (
	addr            string
	dir             string
	base            string
	defaultTemplate bool = true
)

type Page struct {
	filePath string
	meta     map[string]interface{}
}

type Data struct {
	Content template.HTML
	Title   string
	History []History
}

type History struct {
	object.Commit
	EmailHash string
}

type wikilinkResolver struct{}

/*
A wikilink (or internal link) is a link from one page to another page.

Links are enclosed in doubled square brackets:

	    [[1234]] is seen as "1234" in text and links to (the top of) page "1234".
		![[foo.png]] is the embedded link form to add images to a document.
		![[foo.png|alt text]] add alt text to images
*/
func (wikilinkResolver) ResolveWikilink(n *wikilink.Node) ([]byte, error) {
	_hash := []byte{'#'}
	dest := make([]byte, len(n.Target)+len(_hash)+len(n.Fragment))
	var i int
	if len(n.Target) > 0 {
		i += copy(dest, n.Target)
	}
	if len(n.Fragment) > 0 {
		i += copy(dest[i:], _hash)
		i += copy(dest[i:], n.Fragment)
	}
	return dest[:i], nil
}

// Returns page name - filename without .md extension
func (p *Page) Name() string {
	fileName := filepath.Base(p.filePath)
	return fileName[:len(fileName)-len(filepath.Ext(fileName))]
}

/*
Returns URI for page

	ex.: `/home/wiki/page.md` if `/home/wiki` is `dir` will return `/`
*/
func (p *Page) URI() string {
	fileName := filepath.Base(p.filePath)
	return p.filePath[len(dir) : len(p.filePath)-len(filepath.Ext(fileName))]
}

// Generates error message and rise `log.Fatal“ if err no nil
func riseErr(msg string, err error) {
	if err != nil {
		log.Fatalf("[ERROR] %s: %v", msg, err)
	}
}

// Returns markdown data
func (p *Page) Markdown() []byte {
	b, err := os.ReadFile(p.filePath)
	riseErr(fmt.Sprintf("Error reading file %s", p.filePath), err)
	return b
}

// Returns parsed HTML from Markdown data
func (p *Page) HTML() []byte {
	var buf bytes.Buffer
	context := parser.NewContext()
	// catppuccinStyle := highlighting.
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(
				highlighting.WithFormatOptions(
					chromahtml.WithClasses(true),
				),
			),
			emoji.Emoji,
			meta.Meta,
			&mermaid.Extender{},
			&wikilink.Extender{
				Resolver: wikilinkResolver{},
			},
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			html.WithXHTML(),
		),
	)
	err := md.Convert(p.Markdown(), &buf, parser.WithContext(context))
	riseErr(fmt.Sprintf("Error converting %s to HTML", p.Name()), err)
	p.meta = meta.Get(context)
	return buf.Bytes()
}

// Returns HTTP Handler
func (p *Page) Handler(w http.ResponseWriter, r *http.Request) {
	var data Data
	templates := []string{
		"templates/index.html",
		"templates/footer.html",
		"assets/images/circle-half-stroke-solid.svg",
	}
	_, isHistory := r.URL.Query()["history"]
	if isHistory {
		repo := openRepo(dir)
		history := gitHistory(repo, filepath.Base(p.filePath))
		templates = append(templates, "templates/history.html")
		data = Data{Title: "History - " + p.Name(), History: history}
	} else {
		templates = append(templates, "templates/article.html")
		data = Data{Content: template.HTML(p.HTML()), Title: p.Name()}
	}
	tpl, err := template.ParseFiles(templates...)
	riseErr(fmt.Sprintf("Error parse templates for %s", p.Name()), err)
	err = tpl.Execute(w, data)
	riseErr(fmt.Sprintf("Error execute templates for %s", p.Name()), err)
}

// Redirect to canonical URI
func (p *Page) Redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, p.URI(), http.StatusMovedPermanently)
}

func lookupEnvOrString(key string, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
		handler.ServeHTTP(w, r)
	})
}

func readDir(dir string) []*Page {
	var result []*Page
	files, err := os.ReadDir(dir)
	riseErr(fmt.Sprintf("Error opening %s", dir), err)

	for _, file := range files {
		if file.IsDir() {
			result = append(result, readDir(filepath.Join(dir, file.Name()))...)
			continue
		}
		if filepath.Ext(file.Name()) == ".md" {
			result = append(result, &Page{filePath: filepath.Join(dir, file.Name())})
		}
	}
	return result
}

func registerPage(p *Page, baseURL string) {
	lowerName := strings.ToLower(p.Name())
	http.HandleFunc(filepath.Join(baseURL, p.URI()), p.Handler)
	http.HandleFunc(filepath.Join(baseURL, strings.Replace(p.URI(), p.Name(), lowerName, -1)), p.Redirect)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	staticDir := filepath.Join(dir, "static")
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/Home", http.StatusMovedPermanently)
		return
	}
	if _, err := os.Stat(staticDir); err == nil && r.URL.Path != "/" {
		fs := http.FileServer(http.Dir(staticDir))
		fs.ServeHTTP(w, r)
		return
	}
	http.NotFoundHandler().ServeHTTP(w, r)
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	fileBytes, err := os.ReadFile("assets/images/w.ico")
	riseErr("Error reading favicon file", err)
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(fileBytes)
}

func gitHistory(r *git.Repository, f string) []History {
	var history []History

	head, err := r.Head()
	riseErr("Error get HEAD", err)

	commit, err := r.CommitObject(head.Hash())
	riseErr("Error get HEAD commit", err)

	tree, err := commit.Tree()
	riseErr("Error get HEAD commit tree", err)

	for _, entry := range tree.Entries {
		log.Println(entry.Name)
	}

	f = filepath.Clean(f)

	entry, err := tree.FindEntry(f)
	riseErr(fmt.Sprintf("Error find %s in HEAD commit tree", f), err)

	iter, err := r.Log(&git.LogOptions{
		From: commit.Hash,
	})
	riseErr("Error get commit history", err)

	err = iter.ForEach(func(c *object.Commit) error {
		t, err := c.Tree()
		riseErr("Error get commit tree", err)

		e, err := t.FindEntry(f)
		if err != nil {
			return nil
		}

		if e.Hash == entry.Hash {
			h := md5.Sum([]byte(c.Author.Email))
			emailHash := hex.EncodeToString(h[:])
			history = append(history, History{*c, emailHash})
		}
		return nil
	})

	return history
}

func openRepo(path string) *git.Repository {
	r, err := git.PlainOpen(path)
	riseErr(fmt.Sprintf("Error open git repo in %s", path), err)
	return r
}

func init() {
	flag.StringVar(&addr, "addr", lookupEnvOrString("SAM_ADDR", "127.0.0.1:6250"), "address to listen")
	flag.StringVar(&dir, "dir", lookupEnvOrString("SAM_DIR", "./"), "root directory")
	flag.StringVar(&base, "base", lookupEnvOrString("SAM_BASE", "/"), "server base URL")
	flag.Parse()
}

func main() {
	log.Println("Starting Sam...")
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		riseErr(fmt.Sprintf("Error expand user home directory path for %s", dir), err)
		dir = filepath.Join(home, dir[2:])
	}
	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/favicon.ico", faviconHandler)
	log.Printf("Reading directory %s...", dir)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		log.Println("Custom template found")
		defaultTemplate = false
	} else {
		http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets/"))))
	}
	redirectsFile := filepath.Join(dir, "redirects.conf")
	if _, err := os.Stat(redirectsFile); err == nil {
		log.Println("Redirects config found")
		fp, err := os.Open(redirectsFile)
		riseErr("Error open redirects file", err)
		redirects := map[string]string{}
		err = yaml.NewDecoder(fp).Decode(redirects)
		riseErr("Error decoding redirects file", err)

		for src, dst := range redirects {
			log.Printf("Registering redirect %s -> %s", filepath.Join(base, src), filepath.Join(base, dst))
			http.HandleFunc(filepath.Join(base, src), func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, filepath.Join(base, dst), http.StatusMovedPermanently)
			})
		}
	}
	pages := readDir(dir)
	log.Printf("Found %d pages", len(pages))
	log.Println("Registering pages...")
	for _, page := range pages {
		log.Printf("Registering page %s", page.URI())
		registerPage(page, base)
	}
	log.Println("Starting server...")
	log.Printf("Listen address: %s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequest(http.DefaultServeMux)))
}
