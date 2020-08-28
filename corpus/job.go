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
	"masm/jobs"
)

// JobInfo collects information about corpus data synchronization job
type JobInfo struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	CorpusID string        `json:"corpusId"`
	Start    jobs.JSONTime `json:"start"`
	Finish   jobs.JSONTime `json:"finish"`
	Error    string        `json:"error"`
	Result   *syncResponse `json:"result"`
}

func (j *JobInfo) GetID() string {
	return j.ID
}

func (j *JobInfo) GetType() string {
	return j.Type
}

func (j *JobInfo) GetStartDT() jobs.JSONTime {
	return j.Start
}

func (j *JobInfo) GetCorpus() string {
	return j.CorpusID
}

func (j *JobInfo) IsFinished() bool {
	return !j.Finish.IsZero()
}

func (j *JobInfo) SetFinished() {
	j.Finish = jobs.CurrentDatetime()
}

func (j *JobInfo) CompactVersion() jobs.JobInfoCompact {
	item := jobs.JobInfoCompact{
		ID:       j.ID,
		Type:     j.Type,
		CorpusID: j.CorpusID,
		Start:    j.Start,
		Finish:   j.Finish,
		OK:       true,
	}
	if j.Error != "" || (j.Result != nil && !j.Result.OK) {
		item.OK = false
	}
	return item
}