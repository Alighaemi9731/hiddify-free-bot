package bot

import (
	"compress/gzip"
	"io"
	"os"
)

// gunzip decompresses src (a .gz file) into dst.
func gunzip(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer zr.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, zr); err != nil { //nolint:gosec
		return err
	}
	return out.Sync()
}
