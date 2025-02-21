// Copyright 2019 Tomas Machalek <tomas.machalek@gmail.com>
// Copyright 2019 Institute of the Czech National Corpus,
//                Faculty of Arts, Charles University
//   This file is part of CNC-MASM.
//
//  CNC-MASM is free software: you can redistribute it and/or modify
//  it under the terms of the GNU General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  CNC-MASM is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU General Public License for more details.
//
//  You should have received a copy of the GNU General Public License
//  along with CNC-MASM.  If not, see <https://www.gnu.org/licenses/>.

package corpus

import (
	"errors"
	"fmt"
	"masm/v3/mango"
	"path/filepath"
	"strings"

	"github.com/czcorpus/cnc-gokit/fs"
)

var (
	CorpusNotFound = errors.New("corpus not found")
)

// FileMappedValue is an abstraction of a configured file-related
// value where 'Value' represents the value to be inserted into
// some configuration and may or may not be actual file path.
type FileMappedValue struct {
	Value        string  `json:"value"`
	Path         string  `json:"-"`
	FileExists   bool    `json:"exists"`
	LastModified *string `json:"lastModified"`
	Size         int64   `json:"size"`
}

func (fmv FileMappedValue) TruePath() string {
	return fmv.Path
}

func (fmv FileMappedValue) VisiblePath() string {
	return fmv.Value
}

// RegistryConf wraps registry configuration related info
type RegistryConf struct {
	Paths        []FileMappedValue   `json:"paths"`
	Vertical     FileMappedValue     `json:"vertical"`
	Encoding     string              `json:"encoding"`
	SubcorpAttrs map[string][]string `json:"subcorpAttrs"`
}

// Data wraps information about indexed corpus data/files
type Data struct {
	Size         int64           `json:"size"`
	Path         FileMappedValue `json:"path"`
	ManateeError *string         `json:"manateeError"`
}

type IndexedData struct {
	Primary *Data `json:"primary"`
	Limited *Data `json:"omezeni,omitempty"`
}

// Info wraps information about a corpus installation
type Info struct {
	ID             string       `json:"id"`
	IndexedData    IndexedData  `json:"indexedData"`
	IndexedStructs []string     `json:"indexedStructs"`
	RegistryConf   RegistryConf `json:"registry"`
}

// InfoError is a general corpus data information error.
type InfoError struct {
	error
}

type CorpusError struct {
	error
}

// bindValueToPath creates a new FileMappedValue instance
// using 'value' argument. Then it tests whether the
// 'path' exists and if so then it sets related properties
// (FileExists, LastModified, Size) to proper values
func bindValueToPath(value, path string) (FileMappedValue, error) {
	ans := FileMappedValue{Value: value, Path: path}
	isFile, err := fs.IsFile(path)
	if err != nil {
		return ans, err
	}
	if isFile {
		mTime, err := fs.GetFileMtime(path)
		if err != nil {
			return ans, err
		}
		mTimeString := mTime.Format("2006-01-02T15:04:05-0700")
		size, err := fs.FileSize(path)
		if err != nil {
			return ans, err
		}
		ans.FileExists = true
		ans.LastModified = &mTimeString
		ans.Size = size
	}
	return ans, nil
}

// getCorpusInfo obtains misc. information about
// a provided corpus. Errors returned by Manatee
// are not returned by the function as they are
// just attached to the returned `Data` value and
// considered being a part of function's "reporting"
func getCorpusInfo(corpus *mango.GoCorpus) (*Data, error) {
	ans := new(Data)
	var err error
	ans.Size, err = mango.GetCorpusSize(corpus)
	if err != nil {
		errStr := err.Error()
		ans.ManateeError = &errStr
		return ans, nil
	}
	corpDataPath, err := mango.GetCorpusConf(corpus, "PATH")
	if err != nil {
		return nil, InfoError{err}
	}
	dataDirPath := filepath.Clean(corpDataPath)
	dataDirMtime, err := fs.GetFileMtime(dataDirPath)
	if err != nil {
		return nil, InfoError{err}
	}
	dataDirMtimeR := dataDirMtime.Format("2006-01-02T15:04:05-0700")
	isDir, err := fs.IsDir(dataDirPath)
	if err != nil {
		return nil, InfoError{err}
	}
	size, err := fs.FileSize(dataDirPath)
	if err != nil {
		return nil, InfoError{err}
	}
	ans.Path = FileMappedValue{
		Value:        dataDirPath,
		LastModified: &dataDirMtimeR,
		FileExists:   isDir,
		Size:         size,
	}
	return ans, nil
}

func openCorpus(regPath string) (*mango.GoCorpus, error) {
	corp, err := mango.OpenCorpus(regPath)
	if err != nil {
		if strings.Contains(err.Error(), "CorpInfoNotFound") {
			return nil, CorpusNotFound

		}
		return nil, fmt.Errorf("Manatee failed to open %s: %w", regPath, err)
	}
	return corp, err
}

// GetCorpusInfo provides miscellaneous corpus installation information mostly
// related to different data files.
// It should return an error only in case Manatee or filesystem produces some
// error (i.e. not in case something is just not found).
func GetCorpusInfo(corpusID string, setup *CorporaSetup, tryLimited bool) (*Info, error) {
	ans := &Info{ID: corpusID}
	ans.IndexedData = IndexedData{}
	ans.RegistryConf = RegistryConf{Paths: make([]FileMappedValue, 0, 10)}
	ans.RegistryConf.SubcorpAttrs = make(map[string][]string)

	corpReg1 := setup.GetFirstValidRegistry(corpusID, CorpusVariantPrimary.SubDir())
	value, err := bindValueToPath(corpReg1, corpReg1)
	if err != nil {
		return nil, InfoError{err}
	}
	ans.RegistryConf.Paths = append(ans.RegistryConf.Paths, value)

	corp1, err := openCorpus(corpReg1)
	if err != nil {
		return nil, InfoError{err}
	}
	defer mango.CloseCorpus(corp1)
	corp1Info, err := getCorpusInfo(corp1)
	if err != nil {
		return nil, InfoError{fmt.Errorf("Failed to get info about %s: %w", corpReg1, err)}
	}
	ans.IndexedData.Primary = corp1Info

	if tryLimited {
		corpReg2 := setup.GetFirstValidRegistry(corpusID, CorpusVariantLimited.SubDir())
		corp2, err := openCorpus(corpReg2)
		if err != nil {
			return nil, InfoError{err}
		}
		defer mango.CloseCorpus(corp2)
		corp2Info, err := getCorpusInfo(corp2)
		if err != nil {
			return nil, InfoError{fmt.Errorf("Failed to get info about %s: %w", corpReg2, err)}
		}
		ans.IndexedData.Limited = corp2Info
	}

	// -------

	// get encoding
	ans.RegistryConf.Encoding, err = mango.GetCorpusConf(corp1, "ENCODING")
	if err != nil {
		return nil, InfoError{err}
	}

	// parse SUBCORPATTRS
	subcorpAttrsString, err := mango.GetCorpusConf(corp1, "SUBCORPATTRS")
	if err != nil {
		return nil, InfoError{err}
	}
	if subcorpAttrsString != "" {
		for _, attr1 := range strings.Split(subcorpAttrsString, "|") {
			for _, attr2 := range strings.Split(attr1, ",") {
				split := strings.Split(attr2, ".")
				ans.RegistryConf.SubcorpAttrs[split[0]] = append(ans.RegistryConf.SubcorpAttrs[split[0]], split[1])
			}
		}
	}

	unparsedStructs, err := mango.GetCorpusConf(corp1, "STRUCTLIST")
	if err != nil {
		return nil, InfoError{err}
	}
	if unparsedStructs != "" {
		structs := strings.Split(unparsedStructs, ",")
		ans.IndexedStructs = make([]string, len(structs))
		for i, st := range structs {
			ans.IndexedStructs[i] = st
		}
	}

	// try registry's VERTICAL
	regVertical, err := mango.GetCorpusConf(corp1, "VERTICAL")
	if err != nil {
		return nil, InfoError{err}
	}
	ans.RegistryConf.Vertical, err = bindValueToPath(regVertical, regVertical)
	if err != nil {
		return nil, InfoError{err}
	}

	return ans, nil
}

func OpenCorpus(corpusID string, setup *CorporaSetup) (*mango.GoCorpus, error) {
	for _, regPathRoot := range setup.RegistryDirPaths {
		regPath := filepath.Join(regPathRoot, corpusID)
		isFile, err := fs.IsFile(regPath)
		if err != nil {
			return nil, InfoError{err}
		}
		if isFile {
			corp, err := mango.OpenCorpus(regPath)
			if err != nil {
				if strings.Contains(err.Error(), "CorpInfoNotFound") {
					return nil, CorpusNotFound

				}
				return nil, CorpusError{err}
			}
			return corp, nil
		}
	}
	return nil, CorpusNotFound
}

func GetCorpusAttrs(corpusID string, setup *CorporaSetup) ([]string, error) {

	corp, err := OpenCorpus(corpusID, setup)
	if err != nil {
		return []string{}, err
	}

	unparsedStructs, err := mango.GetCorpusConf(corp, "ATTRLIST")
	if err != nil {
		return nil, InfoError{err}
	}
	if unparsedStructs != "" {
		return strings.Split(unparsedStructs, ","), nil
	}
	return nil, nil
}
