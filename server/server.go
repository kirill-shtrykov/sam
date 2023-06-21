package server

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
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

// ErrMetaNotFound is returned by ReadMeta when provided file
// doesn't contains metadata block
var ErrMetaNotFound = errors.New("Metadata not found")

// Returns default templates for `http/templates` in HTTP handlers
func DefaultTemplates() []string {
	return []string{
		"templates/index.html",
		"templates/footer.html",
		"assets/images/circle-half-stroke-solid.svg",
	}
}

// Represents data structure for history pages
type History struct {
	object.Commit
	EmailHash string
}

// Represents data for links on tags pages
type Link struct {
	Name string
	URI  string
}

// Represents data for template rendering
type Data struct {
	Content template.HTML
	Title   string
	History []History
	Links   []Link
}

type wikilinkResolver struct{}

// A wikilink (or internal link) is a link from one page to another page.
// Links are enclosed in doubled square brackets:
//
//	    [[1234]] is seen as "1234" in text and links to (the top of) page "1234".
//		![[foo.png]] is the embedded link form to add images to a document.
//		![[foo.png|alt text]] add alt text to images
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

// Represents page meta
type Meta struct {
	Tags  []string `yaml:"tags"`
	Draft bool     `yaml:"draft"`
}

// Represents wiki page
type Page struct {
	Name     string
	FilePath string
	Meta     *Meta
	URI      string
}

// Read page file and returns parsed Markdown in HTML or Error
func (p *Page) HTML() ([]byte, error) {
	var buf bytes.Buffer
	b, err := os.ReadFile(p.FilePath)
	if err != nil {
		return nil, fmt.Errorf("error read page file: %v", err)
	}

	gm := goldmark.New(
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

	err = gm.Convert(b, &buf)
	if err != nil {
		return nil, fmt.Errorf("error converting %s to HTML: %v", p.Name, err)
	}

	return buf.Bytes(), nil
}

// Base HTTP Handler for wiki page
func (p *Page) Handler(w http.ResponseWriter, r *http.Request) {
	var data Data
	var tpl string
	_, isHistory := r.URL.Query()["history"]
	if isHistory {
		d := filepath.Dir(p.FilePath)
		r, err := git.PlainOpen(d)
		if err != nil {
			log.Printf("error open git repo in %s: %v", d, err)
			http.Error(w, "Internal server error", 500)
			return
		}
		history, err := gitHistory(r, filepath.Base(p.FilePath))
		if err != nil {
			log.Printf("error read git histroy for in %s: %v", p.FilePath, err)
			http.Error(w, "Internal server error", 500)
			return
		}
		tpl = "templates/history.html"
		data = Data{Title: "History - " + p.Name, History: history}
	} else {
		tpl = "templates/article.html"
		html, err := p.HTML()
		if err != nil {
			log.Printf("error read HTML for %s: %v", p.Name, err)
			http.Error(w, "Internal server error", 500)
			return
		}
		data = Data{Content: template.HTML(html), Title: p.Name}
	}
	t, err := template.ParseFiles(append(DefaultTemplates(), tpl)...)
	if err != nil {
		log.Printf("error parse templates for %s: %v", p.Name, err)
		http.Error(w, "Internal server error", 500)
	}
	err = t.Execute(w, data)
	if err != nil {
		log.Printf("error execut template for %s: %v", p.Name, err)
		http.Error(w, "Internal server error", 500)
		return
	}
}

// Redirect to canonical URI
func (p *Page) Redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, p.URI, http.StatusMovedPermanently)
}

// Register HTTP handlers
func (p *Page) Register(baseURL string) {
	lowerName := strings.ToLower(p.Name)
	http.HandleFunc(filepath.Join(baseURL, p.URI), p.Handler)
	http.HandleFunc(filepath.Join(baseURL, strings.Replace(p.URI, p.Name, lowerName, -1)), p.Redirect)
}

// Handler for root path
// Provide wiki root directory to serve static
type RootHandler struct {
	dir string // Wiki root directory
}

func (h *RootHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.Redirect(w, r, "/Home", http.StatusMovedPermanently)
		return
	}

	staticDir := filepath.Join(h.dir, "static")
	if _, err := os.Stat(staticDir); err == nil && r.URL.Path != "/" {
		fs := http.FileServer(http.Dir(staticDir))
		fs.ServeHTTP(w, r)
		return
	}
	http.NotFoundHandler().ServeHTTP(w, r)
}

// Handler for favicon.ico
func faviconHandler(w http.ResponseWriter, r *http.Request) {
	fileBytes, err := os.ReadFile("assets/images/w.ico")
	if err != nil {
		log.Printf("error reading favicon file: %v", err)
		http.Error(w, "Internal server error", 500)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(fileBytes)
}

// Generates page history from git
func gitHistory(r *git.Repository, f string) ([]History, error) {
	var history []History

	iter, err := r.Log(&git.LogOptions{
		FileName: &f,
	})
	if err != nil {
		return nil, fmt.Errorf("Error get history: %v", err)
	}

	err = iter.ForEach(func(c *object.Commit) error {
		h := md5.Sum([]byte(c.Author.Email))
		emailHash := hex.EncodeToString(h[:])
		history = append(history, History{*c, emailHash})

		return nil
	})

	return history, nil
}

// Represent tag
// Contains name and list of pages links
type Tag struct {
	Name  string
	Pages []*Page
}

// Get links for tag pages
func (t *Tag) Links() *[]Link {
	var l []Link
	for _, p := range t.Pages {
		l = append(l, Link{Name: p.Name, URI: p.URI})
	}
	return &l
}

// HTTP handler for tag
func (t *Tag) Handler(w http.ResponseWriter, r *http.Request) {
	data := Data{
		Title: t.Name,
		Links: *t.Links(),
	}
	tpl, err := template.ParseFiles(
		"templates/index.html",
		"templates/footer.html",
		"templates/tags.html",
		"assets/images/circle-half-stroke-solid.svg")
	if err != nil {
		log.Printf("error parse templates for %s: %v", t.Name, err)
		http.Error(w, "Internal server error", 500)
	}
	err = tpl.Execute(w, data)
	if err != nil {
		log.Printf("error execut template for %s: %v", t.Name, err)
		http.Error(w, "Internal server error", 500)
		return
	}
}

// All tags from pages
type Tags struct {
	tags []*Tag
}

// Get links for tags page
func (t *Tags) Links() *[]Link {
	var l []Link
	for _, n := range t.tags {
		l = append(l, Link{Name: n.Name, URI: "/tags/" + n.Name})
	}
	return &l
}

// Add new tag
func (t *Tags) Add(n string) error {
	if t.Get(n) != nil {
		return errors.New("tag already exists")
	}
	tag := Tag{Name: n}
	t.tags = append(t.tags, &tag)
	return nil
}

// Get tag by name
func (t *Tags) Get(n string) *Tag {
	for _, tag := range t.tags {
		if tag.Name == n {
			return tag
		}
	}
	return nil
}

// Add page to tag
func (t *Tags) Update(n string, p *Page) error {
	tag := t.Get(n)
	if tag == nil {
		return errors.New(fmt.Sprintf("tag %s not found", n))
	}
	tag.Pages = append(tag.Pages, p)
	return nil
}

// Root HTTP handler for tags
func (t *Tags) Handler(w http.ResponseWriter, r *http.Request) {
	data := Data{
		Title: "Tags",
		Links: *t.Links(),
	}
	tpl, err := template.ParseFiles(
		"templates/index.html",
		"templates/footer.html",
		"templates/tags.html",
		"assets/images/circle-half-stroke-solid.svg")
	if err != nil {
		log.Printf("error parse templates for tags: %v", err)
		http.Error(w, "Internal server error", 500)
	}
	err = tpl.Execute(w, data)
	if err != nil {
		log.Printf("error execut template for tags: %v", err)
		http.Error(w, "Internal server error", 500)
		return
	}
}

// Register tags handlers
func (t *Tags) Register(base string) {
	if len(t.tags) > 0 {
		log.Println("Registering root for tags...")
		http.HandleFunc(filepath.Join(base, "/tags"), t.Handler)
		for _, tag := range t.tags {
			log.Printf("Registering tag %s...", tag.Name)
			http.HandleFunc(filepath.Join(base, "/tags/"+tag.Name), tag.Handler)
		}
	}
}

// Read wiki git dir and generates `Pages`
func readDir(dir string) ([]*Page, error) {
	var pages []*Page
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("Error opening %s: %v", dir, err)
	}

	for _, file := range files {
		name := file.Name()
		if file.IsDir() {
			p, err := readDir(filepath.Join(dir, name))
			if err != nil {
				return nil, fmt.Errorf("Error reading %s: %v", name, err)
			}
			pages = append(pages, p...)
			continue
		}
		if filepath.Ext(name) == ".md" {
			n := name[:len(name)-len(filepath.Ext(name))]
			fpath := filepath.Join(dir, name)
			uri := fpath[len(dir) : len(fpath)-len(filepath.Ext(name))]
			fp, err := os.Open(fpath)
			if err != nil {
				return nil, fmt.Errorf("Error reading %s: %v", n, err)
			}
			meta, err := readMeta(fp)
			if err != nil {
				if errors.Is(err, ErrMetaNotFound) {
					log.Printf("Metadata for %s not found", name)
				} else {
					return nil, fmt.Errorf("Error reading meta for %s: %v", n, err)
				}
			}
			var m Meta
			yaml.Unmarshal([]byte(strings.Join(meta, "\n")), &m)
			pages = append(pages, &Page{Name: n, FilePath: fpath, URI: uri, Meta: &m})
		}
	}
	return pages, nil
}

// Read metadata from file and returns it or error
func readMeta(r io.Reader) ([]string, error) {
	var found bool
	var meta []string

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if sc.Text() == "" {
			continue
		}
		if !found {
			if sc.Text() == "---" {
				found = true
				continue
			}
			return nil, ErrMetaNotFound
		}
		if found && sc.Text() == "---" {
			return meta, nil
		}
		meta = append(meta, sc.Text())
	}
	return nil, sc.Err()
}

func initWiki(dir string, base string) error {
	log.Println("Register root handler")
	r := RootHandler{dir: dir}
	http.HandleFunc("/", r.Handle)

	log.Println("Register favicon handler")
	http.HandleFunc("/favicon.ico", faviconHandler)

	log.Printf("Reading directory %s...", dir)
	log.Println("Register assets handler")
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("assets/"))))

	// Try to read redirects config
	rc := filepath.Join(dir, "redirects.conf")
	if _, err := os.Stat(rc); err == nil {
		log.Println("Redirects config found")
		fp, err := os.Open(rc)
		if err != nil {
			return fmt.Errorf("Error open redirects file: %v", err)
		}
		redirects := map[string]string{}
		err = yaml.NewDecoder(fp).Decode(redirects)
		if err != nil {
			return fmt.Errorf("Error decoding redirects file: %v", err)
		}

		for src, dst := range redirects {
			log.Printf("Registering redirect %s -> %s", filepath.Join(base, src), filepath.Join(base, dst))
			http.HandleFunc(filepath.Join(base, src), func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, filepath.Join(base, dst), http.StatusMovedPermanently)
			})
		}
	}

	pages, err := readDir(dir)
	if err != nil {
		return fmt.Errorf("Error reading wiki directory: %v", err)
	}
	log.Printf("Found %d pages", len(pages))
	log.Println("Registering pages...")
	tags := &Tags{}
	for _, p := range pages {
		log.Printf("Registering page %s", p.URI)
		p.Register(base)
		for _, t := range p.Meta.Tags {
			tags.Add(t)
			tags.Update(t, p)
		}
	}
	log.Println("Registering tags...")
	tags.Register(base)
	return nil
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
		handler.ServeHTTP(w, r)
	})
}

func Run(addr string, dir string, base string) {
	log.Println("Starting Sam...")
	initWiki(dir, base)
	log.Println("Starting server...")
	log.Printf("Listen address: %s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequest(http.DefaultServeMux)))
}
