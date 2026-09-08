package backup

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func ZipFolder(source, target string) (err error) {
	zipFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := zipFile.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()

	archive := zip.NewWriter(zipFile)
	defer func() {
		if cerr := archive.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("finalize zip: %w", cerr)
		}
	}()

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		return addFile(archive, relPath, path, info)
	})
}

// compressionFor stores pg_dump custom-format files as they are: they are
// already zlib-compressed, so deflating them again costs a full pass over the
// archive for about 8%. Everything else — globals.sql in particular — is plain
// text and compresses well.
func compressionFor(name string) uint16 {
	if filepath.Ext(name) == ".backup" {
		return zip.Store
	}
	return zip.Deflate
}

func addFile(archive *zip.Writer, name, path string, info os.FileInfo) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer, err := archive.CreateHeader(&zip.FileHeader{
		Name:     name,
		Method:   compressionFor(name),
		Modified: info.ModTime(),
	})
	if err != nil {
		return err
	}
	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("add %s to zip: %w", name, err)
	}
	return nil
}
