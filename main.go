// Copyright 2014 The Azul3D Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// TODO(slimsag): Types in package documentation should link to eachother.
// TODO(slimasg): Functions and methods should link to their source on GitHub.

const (
	contentDirName   = "content"
	pagesDirName     = "pages"
	templatesDirName = "templates"
	rootDir          = "src/azul3d.org/cmd/webgen.dev"
	defaultOutDir    = "src/github.com/azul3d/azul3d.github.io"
	pkgDocTemplate   = "pkgdoc.tmpl"
	pkgDocOutDir     = ""
	pkgIndexTemplate = "pkgindex.tmpl"
	pkgIndexOut      = "/packages.html"
	importDomain     = "azul3d.org"
	githubOrg        = "azul3d"

	// Default title to use for rendered Markdown pages when one is not found
	// properly in the file.
	mdDefaultTitle = "Azul3D"

	// Title to append to rendered Markdown pages when one is properly found in
	// the file.
	mdAppendTitle = " - Azul3D"
)

func cleanPath(s string) string {
	s = strings.Replace(s, absRootDir, "$WORK", 1)
	s = strings.Replace(s, *outDir, "$OUT", 1)
	return s
}

var (
	GOPATH           = os.Getenv("GOPATH")
	absRootDir       = filepath.Join(GOPATH, rootDir)
	absDefaultOutDir = filepath.Join(GOPATH, defaultOutDir)
	outDir           = flag.String("out", absDefaultOutDir, "output directory to place generated files")
	cleanOutDir      = flag.Bool("clean", true, "clean output directory (deletes all files)")
	httpAddr         = flag.String("http", "", "port to serve files over HTTP after generation, e.g. :8080")
	updateFlag       = flag.Bool("update", true, "update scanned repositories using go get -u")
	docsFlag         = flag.Bool("docs", true, "generate package documentation (links broken otherwise)")
	auth             = flag.Bool("auth", true, "authenticate with GitHub using $GITHUB_API_TOKEN")
	pushAfter        = flag.Bool("push", true, "run git add, commit, and push in the output directory after generation")

	tmplRoot *template.Template
)

func main() {
	// Configure logging and parse command line flags.
	log.SetFlags(0)
	flag.Parse()

	// Check for invalid GOPATH's.
	if len(GOPATH) == 0 {
		log.Fatal("GOPATH is invalid.")
	}

	// If there is no GitHub API token but they want authentication -- it's a
	// fatal error.
	if len(API_TOKEN) == 0 && *auth {
		log.Println("$GITHUB_API_TOKEN not set to a GitHub API token!")
		log.Fatal("Continue without authentication using -auth=false")
	}

	// Initialize a pool of 16 GitHub clients.
	ghInitClients(API_TOKEN, 16)

	// Log the working and output directory paths.
	log.Println("WORK =", strings.Replace(absRootDir, GOPATH, "$GOPATH", -1))
	log.Println("OUT =", strings.Replace(*outDir, GOPATH, "$GOPATH", -1))

	// If desired, we clean the output directory of all existing contents
	// except .git file paths.
	if *cleanOutDir {
		log.Println("rm -rf", cleanPath(*outDir))
		err := rmIgnoreGit(*outDir)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Copy the content folder to the output directory.
	content := filepath.Join(absRootDir, contentDirName)
	err := cp(content, *outDir)
	if err != nil {
		log.Fatal(err)
	}

	// Initialize the root template.
	//
	// TODO(slimsag): remove makeSection when no longer needed due to Markdown.
	tmplRoot, err = template.New("root").Funcs(map[string]interface{}{
		"section":      makeSection,
		"filepathJoin": filepath.Join,
	}).ParseGlob(filepath.Join(absRootDir, templatesDirName, "*.tmpl"))
	if err != nil {
		log.Fatal(err)
	}

	// Execute each page template as needed.
	//
	// TODO(slimsag): Remove this entirely after moving to Markdown.
	pagesDir := filepath.Join(absRootDir, pagesDirName)
	err = filepath.Walk(pagesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Only touch .tmpl files.
		if filepath.Ext(path) != ".tmpl" {
			return nil
		}

		// Open template file (or folder).
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		// If not a template file, don't do anything.
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}

		// Grab the relative directory path.
		dir := strings.TrimPrefix(path, absRootDir)
		dir = strings.TrimPrefix(dir, string(os.PathSeparator))
		dir = filepath.Dir(dir)

		// Grab template name.
		name := strings.TrimSuffix(filepath.Base(path), ".tmpl")

		// Create output directory structure.
		dirNoPages := strings.TrimPrefix(dir, pagesDirName)
		htmlOut := filepath.Join(*outDir, dirNoPages, name+".html")
		htmlOutDir := filepath.Dir(htmlOut)
		err = os.MkdirAll(htmlOutDir, os.ModeDir|os.ModePerm)
		if err != nil {
			log.Println("mkdir", cleanPath(htmlOutDir))
			return err
		}

		// Read file data.
		fdata, err := ioutil.ReadAll(f)
		if err != nil {
			return err
		}

		// Create and load template.
		tmplName := filepath.Join(dirNoPages, name)
		tmpl, err := tmplRoot.New(tmplName).Parse(string(fdata))
		if err != nil {
			return err
		}

		// Create template output HTML file.
		of, err := os.Create(htmlOut)
		if err != nil {
			return err
		}

		// Execute template.
		log.Println("execute", tmplName, ">", cleanPath(htmlOut))
		err = tmpl.ExecuteTemplate(of, tmplName, nil)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// Generate package documentation.
	err = generateDocs()
	if err != nil {
		log.Fatal(err)
	}

	// Generate all articles.
	err = mdGenerate([]string{
		"*.md",
		"news/*.md",
		"news/*/*.md",
	}, "article.tmpl", false)
	if err != nil {
		log.Fatal(err)
	}

	// Generate all documentation.
	err = mdGenerate([]string{
		"doc/*.md",
		"doc/*/*.md",
	}, "doc.tmpl", false)
	if err != nil {
		log.Fatal(err)
	}

	// Automatically push the changes to the git repository, if desired.
	if *pushAfter {
		log.Println("Pushing changes to remote...")
		log.Println("    Repo Root:", *outDir)
		err := gitadda(*outDir)
		if err != nil {
			log.Println("    ", err)
		}
		err = gitcommitam(*outDir, "Automatic commit by webgen command line tool.")
		if err != nil {
			log.Println("    ", err)
		}
		err = gitpush(*outDir)
		if err != nil {
			log.Println("    ", err)
		}
	}

	// Start the local HTTP server, if desired.
	if len(*httpAddr) > 0 {
		log.Println("Listening on HTTP", *httpAddr)
		http.Handle("/", http.FileServer(http.Dir(*outDir)))
		err := http.ListenAndServe(*httpAddr, nil)
		if err != nil {
			log.Fatal(err)
		}
	}
}
