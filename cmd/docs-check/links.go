package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// brokenLink is one internal link whose target page, file, or anchor does
// not exist in the built site.
type brokenLink struct {
	// page is the site path of the page that holds the link, such as
	// "/docs/quickstart/".
	page string

	// href is the link as written in the page, entities decoded.
	href string

	// missing names what is wrong: "page", "anchor", or "valid URL".
	missing string
}

// site is the parsed output of one Hugo build.
type site struct {
	dir   string
	ids   map[string]map[string]bool
	hrefs map[string][]string
}

// checkLinks reads the Hugo output in dir and reports every internal link
// whose target page, file, or #anchor is missing, sorted by page and href.
// dir must be built with baseURL "/", so site paths match file paths.
func checkLinks(dir string) ([]brokenLink, error) {
	s, err := loadSite(dir)
	if err != nil {
		return nil, err
	}
	var broken []brokenLink
	for page, hrefs := range s.hrefs {
		for _, href := range hrefs {
			if missing := s.resolve(page, href); missing != "" {
				broken = append(broken, brokenLink{page: page, href: href, missing: missing})
			}
		}
	}
	slices.SortFunc(broken, func(a, b brokenLink) int {
		return strings.Compare(a.page+" "+a.href, b.page+" "+b.href)
	})
	return slices.Compact(broken), nil
}

// loadSite collects the ids and hrefs of every .html file under dir, keyed
// by site path.
func loadSite(dir string) (*site, error) {
	s := &site{dir: dir, ids: map[string]map[string]bool{}, hrefs: map[string][]string{}}
	//nolint:gosec // dir is the caller's own build output, same trust level as any CLI path argument
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".html" {
			return err
		}
		body, err := os.ReadFile(p) //nolint:gosec // p comes from walking the caller's own build output
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		page := sitePath(filepath.ToSlash(rel))
		a, err := scanAttrs(body)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		s.ids[page] = a.ids
		s.hrefs[page] = a.hrefs
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("docs-check: walk %s: %w", dir, err)
	}
	return s, nil
}

// sitePath turns a slash-separated path relative to the build root into
// the path a browser requests: "docs/a/index.html" is "/docs/a/".
func sitePath(rel string) string {
	if rel == "index.html" {
		return "/"
	}
	if dir, ok := strings.CutSuffix(rel, "/index.html"); ok {
		return "/" + dir + "/"
	}
	return "/" + rel
}

// pageAttrs holds the id and href attribute values of one HTML document.
type pageAttrs struct {
	ids   map[string]bool
	hrefs []string
}

// scanAttrs returns every id and href attribute value in an HTML document.
// Tokenizing, not pattern matching, keeps escaped markup inside a code
// block from counting as a link or an anchor.
func scanAttrs(body []byte) (pageAttrs, error) {
	a := pageAttrs{ids: map[string]bool{}}
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); !errors.Is(err, io.EOF) {
				return pageAttrs{}, fmt.Errorf("tokenize: %w", err)
			}
			return a, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			for _, attr := range z.Token().Attr {
				switch attr.Key {
				case "id":
					a.ids[attr.Val] = true
				case "href":
					a.hrefs = append(a.hrefs, attr.Val)
				}
			}
		case html.TextToken, html.EndTagToken, html.CommentToken, html.DoctypeToken:
		}
	}
}

// resolve reports what href, found on page, points to that does not exist:
// "page", "anchor", "valid URL", or "" when the link is fine or external.
func (s *site) resolve(page, href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return "valid URL"
	}
	if u.Scheme != "" || u.Host != "" {
		return ""
	}
	target := (&url.URL{Path: page}).ResolveReference(u).Path
	if dir, ok := strings.CutSuffix(target, "/index.html"); ok {
		target = dir + "/"
	}
	if _, ok := s.ids[target]; !ok {
		switch {
		case path.Ext(target) != "" && path.Ext(target) != ".html":
			//nolint:gosec // a missing file is the finding; Stat reads nothing outside what the link names
			if _, err := os.Stat(filepath.Join(s.dir, filepath.FromSlash(target))); err != nil {
				return "page"
			}
			return ""
		case s.ids[target+"/"] != nil:
			target += "/"
		default:
			return "page"
		}
	}
	if u.Fragment != "" && !s.ids[target][u.Fragment] {
		return "anchor"
	}
	return ""
}
