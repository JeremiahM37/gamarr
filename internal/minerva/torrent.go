package minerva

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
)

type FileMeta struct {
	Index int
	Path  string
	Name  string
	Size  int64
}

type TorrentMeta struct {
	Name     string
	InfoHash string
	Files    []FileMeta
}

// ParseTorrent reads the v1 metadata needed to select files from a torrent.
func ParseTorrent(data []byte) (TorrentMeta, error) {
	root, err := decodeBencode(data)
	if err != nil {
		return TorrentMeta{}, err
	}
	if root.kind != bdict {
		return TorrentMeta{}, errors.New("torrent: top-level value is not a dictionary")
	}

	info, found := dictEntry(root, "info")
	if !found {
		return TorrentMeta{}, errors.New("torrent: missing info dictionary")
	}
	if info.value.kind != bdict {
		return TorrentMeta{}, errors.New("torrent: info is not a dictionary")
	}

	nameValue, found := dictEntry(info.value, "name")
	if !found || nameValue.value.kind != bstring {
		return TorrentMeta{}, errors.New("torrent: missing string name")
	}
	name, err := cleanPathComponent(string(nameValue.value.string))
	if err != nil {
		return TorrentMeta{}, fmt.Errorf("torrent: invalid name: %w", err)
	}

	files, err := parseFiles(info.value, name)
	if err != nil {
		return TorrentMeta{}, err
	}
	sum := sha1.Sum(data[info.valueStart:info.valueEnd])
	return TorrentMeta{
		Name:     path.Base(name),
		InfoHash: hex.EncodeToString(sum[:]),
		Files:    files,
	}, nil
}

func parseFiles(info bvalue, name string) ([]FileMeta, error) {
	length, hasLength := dictEntry(info, "length")
	files, hasFiles := dictEntry(info, "files")
	if hasLength == hasFiles {
		return nil, errors.New("torrent: info must contain exactly one of length or files")
	}
	if hasLength {
		if length.value.kind != binteger || length.value.integer < 0 {
			return nil, errors.New("torrent: length must be a non-negative integer")
		}
		return []FileMeta{{Index: 0, Path: name, Name: path.Base(name), Size: length.value.integer}}, nil
	}
	if files.value.kind != blist {
		return nil, errors.New("torrent: files is not a list")
	}

	result := make([]FileMeta, 0, len(files.value.list))
	for index, file := range files.value.list {
		if file.kind != bdict {
			return nil, fmt.Errorf("torrent: file %d is not a dictionary", index)
		}
		length, found := dictEntry(file, "length")
		if !found || length.value.kind != binteger || length.value.integer < 0 {
			return nil, fmt.Errorf("torrent: file %d has invalid length", index)
		}
		parts, found := dictEntry(file, "path")
		if !found || parts.value.kind != blist {
			return nil, fmt.Errorf("torrent: file %d has invalid path", index)
		}
		pathParts := make([]string, 0, len(parts.value.list))
		for _, part := range parts.value.list {
			if part.kind != bstring {
				return nil, fmt.Errorf("torrent: file %d path contains a non-string", index)
			}
			component, err := cleanPathComponent(string(part.string))
			if err != nil {
				return nil, fmt.Errorf("torrent: file %d has invalid path component: %w", index, err)
			}
			pathParts = append(pathParts, component)
		}
		cleanPath, err := cleanRelativePath(strings.Join(pathParts, "/"))
		if err != nil {
			return nil, fmt.Errorf("torrent: file %d has invalid path: %w", index, err)
		}
		// qB/libtorrent file names are relative to SavePath and include info.name
		// for multi-file torrents. Keep that root in the indexed validation path.
		cleanPath = name + "/" + cleanPath
		result = append(result, FileMeta{
			Index: index,
			Path:  cleanPath,
			Name:  path.Base(cleanPath),
			Size:  length.value.integer,
		})
	}
	return result, nil
}

func dictEntry(dict bvalue, key string) (bdictEntry, bool) {
	for _, entry := range dict.dict {
		if bytes.Equal(entry.key, []byte(key)) {
			return entry, true
		}
	}
	return bdictEntry{}, false
}

func cleanRelativePath(value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("NUL byte")
	}
	if strings.Contains(value, `\`) {
		return "", fmt.Errorf("unsafe Windows path %q", value)
	}
	if hasWindowsVolume(value) {
		return "", fmt.Errorf("unsafe Windows volume path %q", value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." || strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe relative path %q", value)
	}
	return cleaned, nil
}

func cleanPathComponent(value string) (string, error) {
	if value == "" || value == "." || value == ".." {
		return "", fmt.Errorf("unsafe path component %q", value)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("NUL byte")
	}
	if strings.ContainsAny(value, `/\`) || hasWindowsVolume(value) {
		return "", fmt.Errorf("unsafe path component %q", value)
	}
	return value, nil
}

func hasWindowsVolume(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}
