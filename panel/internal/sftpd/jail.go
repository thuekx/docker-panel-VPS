package sftpd

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// jail answers the SFTP requests of one session inside its folder (os.Root: no way out through "..", absolute
// symbolic links or links pointing outside). Links cannot be made, owners cannot be changed, and modes keep only
// the permission bits.
type jail struct {
	root     *os.Root
	uid, gid int
}

// rel turns an SFTP path ("/a/b", already cleaned by the request server) into a root-relative name.
func rel(p string) string {
	c := strings.TrimPrefix(path.Clean("/"+p), "/")
	if c == "" {
		return "."
	}
	return c
}

// own gives a new file or folder to the website's file user.
func (j *jail) own(name string) {
	_ = j.root.Lchown(name, j.uid, j.gid)
}

func (j *jail) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	f, err := j.root.Open(rel(r.Filepath))
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, sftp.ErrSSHFxFailure
	}
	return f, nil
}

func (j *jail) Filewrite(r *sftp.Request) (io.WriterAt, error) { return j.open(r) }

// OpenFile is used when a client opens a file for reading and writing at once.
func (j *jail) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) { return j.open(r) }

func (j *jail) open(r *sftp.Request) (*os.File, error) {
	name := rel(r.Filepath)
	if name == "." {
		return nil, sftp.ErrSSHFxFailure
	}
	p := r.Pflags()
	flag := 0
	switch {
	case p.Read && p.Write:
		flag = os.O_RDWR
	case p.Write:
		flag = os.O_WRONLY
	default:
		flag = os.O_RDONLY
	}
	// O_APPEND is left out on purpose: SFTP writes carry their own offsets (WriteAt refuses O_APPEND files).
	if p.Creat {
		flag |= os.O_CREATE
	}
	if p.Trunc {
		flag |= os.O_TRUNC
	}
	if p.Excl {
		flag |= os.O_EXCL
	}
	_, statErr := j.root.Lstat(name)
	f, err := j.root.OpenFile(name, flag, 0o644)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, sftp.ErrSSHFxFailure
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		j.own(name)
	}
	return f, nil
}

func (j *jail) Filecmd(r *sftp.Request) error {
	name := rel(r.Filepath)
	switch r.Method {
	case "Setstat":
		return j.setstat(name, r)
	case "Rename":
		// SFTP v3 rename does not replace an existing name (like OpenSSH); PosixRename does.
		if _, err := j.root.Lstat(rel(r.Target)); err == nil {
			return sftp.ErrSSHFxFailure
		}
		return j.rename(name, rel(r.Target))
	case "Rmdir":
		fi, err := j.root.Lstat(name)
		if err != nil {
			return err
		}
		if !fi.IsDir() || name == "." {
			return sftp.ErrSSHFxFailure
		}
		return j.root.Remove(name)
	case "Remove":
		fi, err := j.root.Lstat(name)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return sftp.ErrSSHFxFailure
		}
		return j.root.Remove(name)
	case "Mkdir":
		if err := j.root.Mkdir(name, 0o755); err != nil {
			return err
		}
		j.own(name)
		return nil
	case "Symlink", "Link":
		return sftp.ErrSSHFxOpUnsupported
	}
	return sftp.ErrSSHFxOpUnsupported
}

// PosixRename replaces the target (the posix-rename@openssh.com extension most clients use).
func (j *jail) PosixRename(r *sftp.Request) error { return j.rename(rel(r.Filepath), rel(r.Target)) }

func (j *jail) rename(from, to string) error {
	if from == "." || to == "." {
		return sftp.ErrSSHFxFailure
	}
	return j.root.Rename(from, to)
}

func (j *jail) setstat(name string, r *sftp.Request) error {
	a, flags := r.Attributes(), r.AttrFlags()
	if flags.Size {
		f, err := j.root.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		err = f.Truncate(int64(a.Size))
		f.Close()
		if err != nil {
			return err
		}
	}
	if flags.Permissions {
		if err := j.root.Chmod(name, fs.FileMode(a.Mode&0o777)); err != nil {
			return err
		}
	}
	if flags.Acmodtime {
		if err := j.root.Chtimes(name, a.AccessTime(), a.ModTime()); err != nil {
			return err
		}
	}
	// a change of owner is ignored: files stay with the website's file user
	return nil
}

func (j *jail) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	name := rel(r.Filepath)
	switch r.Method {
	case "List":
		d, err := j.root.Open(name)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		ents, err := d.ReadDir(-1)
		if err != nil {
			return nil, err
		}
		out := make(listerAt, 0, len(ents))
		for _, e := range ents {
			if fi, err := e.Info(); err == nil {
				out = append(out, fi)
			}
		}
		return out, nil
	case "Stat":
		fi, err := j.root.Stat(name)
		if err != nil {
			return nil, err
		}
		return listerAt{named{fi, path.Base("/" + name)}}, nil
	case "Readlink":
		t, err := j.root.Readlink(name)
		if err != nil {
			return nil, err
		}
		return listerAt{linkInfo(t)}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

// Lstat answers the lstat request without following a link.
func (j *jail) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	fi, err := j.root.Lstat(rel(r.Filepath))
	if err != nil {
		return nil, err
	}
	return listerAt{fi}, nil
}

// RealPath keeps the client inside: every path is shown from the account's own "/".
func (j *jail) RealPath(p string) (string, error) { return path.Clean("/" + p), nil }

type listerAt []os.FileInfo

func (l listerAt) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if n+int(off) >= len(l) {
		return n, io.EOF
	}
	return n, nil
}

// named shows "/" as "/" rather than ".".
type named struct {
	os.FileInfo
	name string
}

func (n named) Name() string { return n.name }

// linkInfo carries a link target as the name, which is how the request server answers readlink.
type linkInfo string

func (l linkInfo) Name() string       { return string(l) }
func (l linkInfo) Size() int64        { return 0 }
func (l linkInfo) Mode() fs.FileMode  { return fs.ModeSymlink }
func (l linkInfo) ModTime() time.Time { return time.Time{} }
func (l linkInfo) IsDir() bool        { return false }
func (l linkInfo) Sys() any           { return nil }
