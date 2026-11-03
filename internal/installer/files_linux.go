package installer

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func copyFile(source, dest string, mode os.FileMode) (err error) {
	in, err := os.OpenFile(source, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	return copyOpened(in, dest, mode)
}

func copyOpened(in *os.File, dest string, mode os.FileMode) (err error) {
	s, err := in.Stat()
	if err != nil {
		return err
	}
	if !s.Mode().IsRegular() {
		return fmt.Errorf("拒绝复制特殊文件：%s", in.Name())
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(dest)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Chmod(mode); err != nil {
		return err
	}
	return out.Sync()
}

func copyTree(source, dest string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	// 限定在已打开的源目录内，避免遍历期间链接变化让root复制目录外文件。
	return fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("备份拒绝符号链接：%s", rel)
		}
		if d.IsDir() {
			return os.Mkdir(target, 0700)
		}
		in, err := root.OpenFile(rel, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer in.Close()
		return copyOpened(in, target, 0600)
	})
}
