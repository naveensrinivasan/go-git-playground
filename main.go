package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/mholt/archiver"
)

type RepoURL struct {
	Host, Owner, Repo string
}

func (r *RepoURL) String() string {
	return fmt.Sprintf("%s/%s/%s", r.Host, r.Owner, r.Repo)
}

func (r *RepoURL) Type() string {
	return "repo"
}

func (r *RepoURL) Set(s string) error {
	// Allow skipping scheme for ease-of-use, default to https.
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}

	u, e := url.Parse(s)
	if e != nil {
		return e
	}

	const splitLen = 2
	split := strings.SplitN(strings.Trim(u.Path, "/"), "/", splitLen)
	if len(split) != splitLen {
		log.Fatalf("invalid repo flag: [%s], pass the full repository URL", s)
	}

	if len(strings.TrimSpace(split[0])) == 0 || len(strings.TrimSpace(split[1])) == 0 {
		log.Fatalf("invalid repo flag: [%s], pass the full repository URL", s)
	}

	r.Host, r.Owner, r.Repo = u.Host, split[0], split[1]
	return nil
}

func main() {
	blob := os.Getenv("BLOB_URL")

	if blob == "" {
		log.Panic("BLOB_URL env is not set.")
	}

	repo := &RepoURL{}
	err := repo.Set(os.Args[1])
	if err != nil {
		log.Panic(err)
	}

	dir, err := ioutil.TempDir("/home/turris/temp/", repo.Owner+repo.Owner)
	if err != nil {
		log.Panic(err)
	}

	tarDir, err := ioutil.TempDir("/home/turris/temp/", "")
	if err != nil {
		log.Panic(err)
	}

	// defer os.RemoveAll(dir)
	// defer os.RemoveAll(tarDir)

	// Clone the given repository to the given directory
	r, err := git.PlainClone(dir, false, &git.CloneOptions{
		URL:      fmt.Sprintf("http://%s/%s/%s", repo.Host, repo.Owner, repo.Repo),
		Depth:    1, // Just fetch the last commit
		Progress: os.Stdout,
	})
	if err != nil {
		log.Panic(err)
	}
	ref, err := r.Head()
	if err != nil {
		log.Panic(err)
	}

	// ... retrieving the commit object
	commit, err := r.CommitObject(ref.Hash())
	if err != nil {
		log.Panic(err)
	}

	// opening the blob
	bucket, e := New(blob)

	if e != nil {
		log.Panic(e)
	}

	lastcommit, err := commit.Author.When.MarshalBinary()
	if err != nil {
		log.Panic(err)
	}

	tpath := path.Join(tarDir, fmt.Sprintf("%s.tar.gz", repo.Repo))
	// storing the last commit to the blob
	err = bucket.Set(fmt.Sprintf("gitcache/%s/%s/lastcommit", repo.Owner, repo.Repo), lastcommit)
	if err != nil {
		log.Panic(err)
	}

	// removing the .git folder as it is not required for the tar ball
	err = os.RemoveAll(path.Join(dir, ".git"))
	if err != nil {
		log.Panic(err)
	}

	// creating an archive
	t := archiver.NewTarGz()

	err = t.Archive([]string{dir}, tpath)

	if err != nil {
		log.Panic(err)
	}

	data, err := ioutil.ReadFile(tpath)
	if err != nil {
		log.Panic(err)
	}

	// storing the archive to the blob
	err = bucket.Set(fmt.Sprintf("gitcache/%s/%s/tar", repo.Owner, repo.Repo), data)
	if err != nil {
		log.Panic(err)
	}
}

func createArchive(files []string, buf io.Writer) error {
	// Create new Writers for gzip and tar
	// These writers are chained. Writing to the tar writer will
	// write to the gzip writer which in turn will write to
	// the "buf" writer
	gw := gzip.NewWriter(buf)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Iterate over files and add them to the tar archive
	for _, file := range files {
		err := addToArchive(tw, file)
		if err != nil {
			return err
		}
	}

	return nil
}

func addToArchive(tw *tar.Writer, filename string) error {
	// Open the file which will be written into the archive
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	// Get FileInfo about our file providing file size, mode, etc.
	info, err := file.Stat()
	if err != nil {
		return err
	}

	// Create a tar Header from the FileInfo data
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return err
	}

	// Use full path as name (FileInfoHeader only takes the basename)
	// If we don't do this the directory strucuture would
	// not be preserved
	// https://golang.org/src/archive/tar/common.go?#L626
	header.Name = filename

	// Write file header to the tar archive
	err = tw.WriteHeader(header)
	if err != nil {
		return err
	}

	// Copy file content to tar archive
	_, err = io.Copy(tw, file)
	if err != nil {
		return err
	}

	return nil
}

func compress(src string, buf io.Writer) error {
	// tar > gzip > buf
	zr := gzip.NewWriter(buf)
	tw := tar.NewWriter(zr)

	// walk through every file in the folder
	filepath.Walk(src, func(file string, fi os.FileInfo, err error) error {
		// generate tar header
		header, err := tar.FileInfoHeader(fi, file)
		if err != nil {
			return err
		}

		// must provide real name
		// (see https://golang.org/src/archive/tar/common.go?#L626)
		header.Name = filepath.ToSlash(file)

		// write header
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		// if not a dir, write file content
		if !fi.IsDir() {
			data, err := os.Open(file)
			if err != nil {
				return err
			}
			if _, err := io.Copy(tw, data); err != nil {
				return err
			}
		}
		return nil
	})

	// produce tar
	if err := tw.Close(); err != nil {
		return err
	}
	// produce gzip
	if err := zr.Close(); err != nil {
		return err
	}
	//
	return nil
}
