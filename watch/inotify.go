// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liyam/fsnotify"
	"github.com/liyam/tail/util"
	"gopkg.in/tomb.v1"
)

// InotifyFileWatcher uses inotify to monitor file changes.
type InotifyFileWatcher struct {
	Filename string
	Size     int64
	w        *fsnotify.Watcher
}

func NewInotifyFileWatcher(filename string, w *fsnotify.Watcher) *InotifyFileWatcher {
	fw := &InotifyFileWatcher{filename, 0, w}
	return fw
}

func (fw *InotifyFileWatcher) BlockUntilExists(t *tomb.Tomb) error {
	dirname := filepath.Dir(fw.Filename)
	filename := fw.Filename

	// for handling filename with no path prefix
	if dirname == "." && !strings.HasPrefix(filename, "./") {
		filename = "./" + filename
	}

	// Watch for new files to be created in the parent directory.
	err := fw.w.Add(dirname)
	if err != nil {
		return err
	}
	defer fw.w.Remove(dirname)

	// Do a real check now as the file might have been created before
	// calling `WatchFlags` above.
	if _, err = os.Stat(fw.Filename); !os.IsNotExist(err) {
		// file exists, or stat returned an error.
		return err
	}

	for {
		select {
		case evt, ok := <-fw.w.Events:
			if !ok {
				return fmt.Errorf("inotify watcher has been closed")
			} else if ((evt.Op & fsnotify.Create) == fsnotify.Create) && (evt.Name == filename) {
				return nil
			}
		case <-t.Dying():
			return tomb.ErrDying
		}
	}
	panic("unreachable")
}

func (fw *InotifyFileWatcher) ChangeEvents(t *tomb.Tomb, fi os.FileInfo) *FileChanges {
	changes := NewFileChanges()

	err := fw.w.Add(fw.Filename)
	if err != nil {
		util.Fatal("Error watching %v: %v", fw.Filename, err)
	}

	fw.Size = fi.Size()

	go func() {
		defer fw.w.Remove(fw.Filename)
		defer changes.Close()

		for {
			prevSize := fw.Size

			var evt fsnotify.Event
			var ok bool

			select {
			case evt, ok = <-fw.w.Events:
				if !ok {
					return
				}
			case <-t.Dying():
				return
			}

			switch {
			case evt.Op&fsnotify.Remove == fsnotify.Remove:
				fallthrough

			case evt.Op&fsnotify.Rename == fsnotify.Rename:
				changes.NotifyDeleted()
				return

			case evt.Op&fsnotify.Write == fsnotify.Write:
				fi, err := os.Stat(fw.Filename)
				if err != nil {
					if os.IsNotExist(err) {
						changes.NotifyDeleted()
						return
					}
					// XXX: report this error back to the user
					util.Fatal("Failed to stat file %v: %v", fw.Filename, err)
				}
				fw.Size = fi.Size()

				if prevSize > 0 && prevSize > fw.Size {
					changes.NotifyTruncated()
				} else {
					changes.NotifyModified()
				}
				prevSize = fw.Size

			case evt.Op&fsnotify.Chmod == fsnotify.Chmod:
				_, err := os.Stat(fw.Filename)
				if err != nil {
					if os.IsNotExist(err) {
						// need to close file to delete inode
						changes.NotifyNeedToClose()
						continue
					}
					// XXX: report this error back to the user
					util.Fatal("Failed to stat file %v: %v", fw.Filename, err)
				}
			}
		}
	}()

	return changes
}
