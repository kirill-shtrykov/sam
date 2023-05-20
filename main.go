package main

import (
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

var (
	addr            string
	dir             string
	base            string
	defaultTemplate bool = true
)

type Page struct {
	filePath string
}

type Data struct {
	Content template.HTML
	Title   string
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

// Returns markdown data
func (p *Page) Markdown() []byte {
	b, err := os.ReadFile(p.filePath)
	if err != nil {
		log.Fatal(err)
	}
	return b
}

// Returns parsed HTML from Markdown data
func (p *Page) HTML() []byte {
	// create markdown parser with extensions
	extensions := parser.CommonExtensions | parser.AutoHeadingIDs | parser.NoEmptyLineBeforeBlock
	par := parser.NewWithExtensions(extensions)
	md := p.Markdown()
	doc := par.Parse(md)
	// create HTML renderer with extensions
	htmlFlags := html.CommonFlags | html.HrefTargetBlank
	opts := html.RendererOptions{Flags: htmlFlags}
	renderer := html.NewRenderer(opts)

	return markdown.Render(doc, renderer)
}

// Returns HTTP Handler
func (p *Page) Handler(w http.ResponseWriter, r *http.Request) {
	data := Data{Content: template.HTML(p.HTML()), Title: p.Name()}
	tpl, err := template.ParseFiles("templates/index.html", "assets/images/circle-half-stroke-solid.svg")
	if err != nil {
		log.Fatal(err)
	}
	err = tpl.Execute(w, data)
	if err != nil {
		log.Fatal(err)
	}
}

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

func readDir(directory string) []*Page {
	var result []*Page
	files, err := os.ReadDir(directory)
	if err != nil {
		log.Fatal(err)
	}

	for _, file := range files {
		if file.IsDir() {
			result = append(result, readDir(filepath.Join(directory, file.Name()))...)
			continue
		}
		if filepath.Ext(file.Name()) == ".md" {
			result = append(result, &Page{filePath: filepath.Join(directory, file.Name())})
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
	if err != nil {
		panic(err)
	}
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(fileBytes)
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
		if err != nil {
			log.Fatal(err)
		}
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
		if err != nil {
			log.Fatal(err)
		}
		redirects := map[string]string{}
		err = yaml.NewDecoder(fp).Decode(redirects)
		if err != nil {
			log.Fatal(err)
		}

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
