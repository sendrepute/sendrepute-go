// Command package creates the allowlist-only local source distribution.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var files = []string{
	"LICENSE",
	"README.md",
	"client.go",
	"go.mod",
	"types.go",
	"examples/basic/main.go",
	"scripts/package.go",
}

func main() {
	// `go run` uses a temporary executable, so locate the module from cwd.
	candidate := "."
	if _, err := os.Stat(filepath.Join(candidate, "go.mod")); err != nil {
		candidate = "integrations/go"
	}
	root, err := filepath.Abs(candidate)
	if err != nil {
		panic(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		panic("run the packager from the repository root or integrations/go")
	}
	outputDir := filepath.Join(root, "dist")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		panic(err)
	}
	output := filepath.Join(outputDir, "sendrepute-go-v0.1.0.zip")
	temporary := output + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	writer := zip.NewWriter(file)
	failed := true
	defer func() {
		if failed {
			_ = writer.Close()
			_ = file.Close()
			_ = os.Remove(temporary)
		}
	}()
	for _, name := range files {
		if err := add(writer, root, name); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	if err := file.Close(); err != nil {
		panic(err)
	}
	if err := os.Rename(temporary, output); err != nil {
		panic(err)
	}
	failed = false
	fmt.Println(output)
}

func add(writer *zip.Writer, root, name string) error {
	path := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = "sendrepute-go-v0.1.0/" + filepath.ToSlash(name)
	header.Method = zip.Deflate
	destination, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(destination, source)
	return err
}
