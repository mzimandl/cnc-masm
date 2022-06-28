// Copyright 2020 Tomas Machalek <tomas.machalek@gmail.com>
// Copyright 2020 Institute of the Czech National Corpus,
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

package liveattrs

import (
	"masm/v3/jobs"
)

type idxJobInfoArgs struct {
	MaxColumns int `json:"maxColumns"`
}

type idxJobResult struct {
	UsedIndexes    []string `json:"usedIndexes"`
	RemovedIndexes []string `json:"removedIndexed"`
}

// IdxUpdateJobInfo collects information about corpus data synchronization job
type IdxUpdateJobInfo struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	CorpusID    string         `json:"corpusId"`
	Start       jobs.JSONTime  `json:"start"`
	Update      jobs.JSONTime  `json:"update"`
	Finished    bool           `json:"finished"`
	Error       jobs.JSONError `json:"error",omitempty`
	NumRestarts int            `json:"numRestarts"`
	Args        idxJobInfoArgs `json:"args"`
	Result      idxJobResult   `json:"result"`
}

func (j *IdxUpdateJobInfo) GetID() string {
	return j.ID
}

func (j *IdxUpdateJobInfo) GetType() string {
	return j.Type
}

func (j *IdxUpdateJobInfo) GetStartDT() jobs.JSONTime {
	return j.Start
}

func (j *IdxUpdateJobInfo) GetNumRestarts() int {
	return j.NumRestarts
}

func (j *IdxUpdateJobInfo) GetCorpus() string {
	return j.CorpusID
}

func (j *IdxUpdateJobInfo) SetFinished() {
	j.Update = jobs.CurrentDatetime()
	j.Finished = true
}

func (j *IdxUpdateJobInfo) IsFinished() bool {
	return j.Finished
}

func (j *IdxUpdateJobInfo) CompactVersion() jobs.JobInfoCompact {
	item := jobs.JobInfoCompact{
		ID:       j.ID,
		Type:     j.Type,
		CorpusID: j.CorpusID,
		Start:    j.Start,
		Update:   j.Update,
		Finished: j.Finished,
		OK:       true,
	}
	if !j.Error.IsEmpty() {
		item.OK = false
	}
	return item
}