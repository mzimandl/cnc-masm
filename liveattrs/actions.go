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
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"masm/v3/api"
	"masm/v3/cncdb"
	"masm/v3/cnf"
	"masm/v3/corpus"
	"masm/v3/db/sqlite"
	"masm/v3/general/collections"
	"masm/v3/jobs"
	"masm/v3/liveattrs/cache"
	"masm/v3/liveattrs/db"
	"masm/v3/liveattrs/db/qbuilder"
	"masm/v3/liveattrs/request/biblio"
	"masm/v3/liveattrs/request/equery"
	"masm/v3/liveattrs/request/fillattrs"
	"masm/v3/liveattrs/request/query"
	"masm/v3/liveattrs/request/response"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	vteCnf "github.com/czcorpus/vert-tagextract/v2/cnf"
	vteDb "github.com/czcorpus/vert-tagextract/v2/db"
	vteLib "github.com/czcorpus/vert-tagextract/v2/library"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	jobType               = "liveattrs"
	emptyValuePlaceholder = "?"
	dfltMaxAttrListSize   = 30
	shortLabelMaxLength   = 30
)

type CreateLiveAttrsReqBody struct {
	Files []string `json:"files"`
}

func loadConf(basePath, corpname string) (*vteCnf.VTEConf, error) {
	return vteCnf.LoadConf(filepath.Join(basePath, fmt.Sprintf("%s.json", corpname)))
}

func arrayShowShortened(data []string) string {
	if len(data) <= 5 {
		return strings.Join(data, ", ")
	}
	ans := make([]string, 5)
	ans[0] = data[0]
	ans[1] = data[1]
	ans[2] = "..."
	ans[3] = data[2]
	ans[4] = data[3]
	return strings.Join(ans, ", ")
}

func groupBibItems(data *response.QueryAns, bibLabel string) {
	grouping := make(map[string]*response.ListedValue)
	entry := data.AttrValues[bibLabel]
	tEntry, ok := entry.([]*response.ListedValue)
	if !ok {
		return
	}
	for _, item := range tEntry {
		val, ok := grouping[item.Label]
		if ok {
			grouping[item.Label].Count += val.Count
			grouping[item.Label].Grouping++

		} else {
			grouping[item.Label] = item
		}
		if grouping[item.Label].Grouping > 1 {
			grouping[item.Label].ID = "@" + grouping[item.Label].Label
		}
	}
	data.AttrValues[bibLabel] = make([]*response.ListedValue, 0, len(grouping))
	for _, v := range grouping {
		entry, ok := (data.AttrValues[bibLabel]).([]*response.ListedValue)
		if !ok {
			continue
		}
		data.AttrValues[bibLabel] = append(entry, v)
	}
}

// createLAConfig creates a new live attribute extraction configuration based
// on provided args.
// note: bibIdAttr and mergeAttrs use dot notation (e.g. "doc.author")
func createLAConfig(
	masmConf *cnf.Conf,
	corpusInfo *corpus.Info,
	corpusDBInfo *corpus.DBInfo,
	atomStructure string,
	bibIdAttr string,
	mergeAttrs []string,
	mergeFn string,
) (*vteCnf.VTEConf, error) {
	newConf := vteCnf.VTEConf{
		Corpus:              corpusInfo.ID,
		ParallelCorpus:      corpusDBInfo.ParallelCorpus,
		AtomParentStructure: "",
		StackStructEval:     false,
		MaxNumErrors:        1000,
		Ngrams:              vteCnf.NgramConf{},
		Encoding:            "UTF-8",
		IndexedCols:         []string{},
		VerticalFile:        corpusInfo.RegistryConf.Vertical.Path,
	}
	newConf.Structures = corpusInfo.RegistryConf.SubcorpAttrs
	if bibIdAttr != "" {
		bibView := vteDb.BibViewConf{}
		bibView.IDAttr = db.ImportKey(bibIdAttr)
		for stru, attrs := range corpusInfo.RegistryConf.SubcorpAttrs {
			for _, attr := range attrs {
				bibView.Cols = append(bibView.Cols, fmt.Sprintf("%s_%s", stru, attr))
			}
		}
		newConf.BibView = bibView
	}
	if atomStructure == "" {
		if len(newConf.Structures) == 1 {
			for k := range newConf.Structures {
				newConf.AtomStructure = k
				break
			}
			log.Print("INFO: no atomStructure, inferred value: ", newConf.AtomStructure)

		} else {
			return nil, fmt.Errorf("no atomStructure specified and the value cannot be inferred due to multiple involved structures")
		}

	} else {
		newConf.AtomStructure = atomStructure
	}
	atomExists := false
	for _, st := range corpusInfo.IndexedStructs {
		if st == newConf.AtomStructure {
			atomExists = true
			break
		}
	}
	if !atomExists {
		return nil, fmt.Errorf("atom structure '%s' does not exist in corpus %s", newConf.AtomStructure, corpusInfo.ID)
	}

	if len(mergeAttrs) > 0 {
		newConf.SelfJoin.ArgColumns = make([]string, len(mergeAttrs))
		for i, argCol := range mergeAttrs {
			tmp := strings.Split(argCol, ".")
			if len(tmp) != 2 {
				return nil, fmt.Errorf("invalid mergeAttr format: %s", argCol)
			}
			newConf.SelfJoin.ArgColumns[i] = tmp[0] + "_" + tmp[1]
			_, ok := newConf.Structures[tmp[0]]
			if ok {
				if !collections.SliceContains(newConf.Structures[tmp[0]], tmp[1]) {
					newConf.Structures[tmp[0]] = append(newConf.Structures[tmp[0]], tmp[1])
				}

			} else {
				newConf.Structures[tmp[0]] = []string{tmp[1]}
			}
		}
		newConf.SelfJoin.GeneratorFn = mergeFn
	}
	newConf.DB = vteDb.Conf{
		Type:           "mysql",
		Host:           masmConf.LiveAttrs.DB.Host,
		User:           masmConf.LiveAttrs.DB.User,
		Password:       masmConf.LiveAttrs.DB.Password,
		PreconfQueries: masmConf.LiveAttrs.DB.PreconfQueries,
	}
	if corpusDBInfo.ParallelCorpus != "" {
		newConf.DB.Name = corpusDBInfo.ParallelCorpus

	} else {
		newConf.DB.Name = corpusInfo.ID
	}
	return &newConf, nil
}

// Actions wraps liveattrs-related actions
type Actions struct {
	// exitEvent channel recieves value once user (or OS) terminates masm process
	exitEvent <-chan os.Signal

	// vteExitEvent stores "exit" channels for running vert-tagextract jobs
	// (max 1 per corpus)
	vteExitEvents map[string]chan os.Signal

	// jobStopChannel receives job ID based on user interaction with job HTTP API in
	// case users asks for stopping the vte process
	jobStopChannel <-chan string

	conf *cnf.Conf

	jobActions *jobs.Actions

	laConfCache *cnf.LiveAttrsBuildConfLoader

	// laDB is a live-attributes-specific database where masm needs full privileges
	laDB *sql.DB

	// cncDB is CNC's main database
	cncDB *cncdb.CNCMySQLHandler

	// eqCache stores results for live-attributes empty queries (= initial text types data)
	eqCache *cache.EmptyQueryCache

	structAttrStats *db.StructAttrUsage

	usageData chan<- db.RequestData
}

func (a *Actions) OnExit() {
	close(a.usageData)
}

func (a *Actions) ViewConf(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]
	conf, err := a.laConfCache.Get(corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionError("error fetching configuration: %s", err), http.StatusBadRequest)
		return
	}
	if conf == nil {
		api.WriteJSONErrorResponse(w, api.NewActionError("Configuration not found"), http.StatusNotFound)
		return
	}
	api.WriteJSONResponse(w, conf)
}

type liveattrsJsonArgs struct {
	VerticalFiles []string `json:"verticalFiles"`
}

// Create starts a process of creating fresh liveattrs data for a a specified corpus.
// URL args:
// * atomStructure - a minimal structure masm will be able to search for (typically 'doc', 'text')
// * noCache - if '1' then masm regenerates data extraction configuration based on actual corpus
//   registry file
// * bibIdAttr - if defined then masm will create bibliography entries with IDs matching values from
//   from referred bibIdAttr values
func (a *Actions) Create(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]
	noCache := false
	if req.URL.Query().Get("noCache") == "1" {
		noCache = true
	}

	var err error
	var conf *vteCnf.VTEConf
	if !noCache {
		conf, err = a.laConfCache.Get(corpusID)
	}

	var jsonArgs liveattrsJsonArgs
	err = json.NewDecoder(req.Body).Decode(&jsonArgs)
	if err != nil && err != io.EOF {
		api.WriteJSONErrorResponse(w, api.NewActionError("liveAttrs generator failed: '%s'", err), http.StatusInternalServerError)
		return

	} else if conf == nil {
		corpusInfo, err := corpus.GetCorpusInfo(corpusID, "", a.conf.CorporaSetup)
		if err != nil {
			api.WriteJSONErrorResponse(w, api.NewActionError("liveAttrs generator failed: '%s'", err), http.StatusInternalServerError)
			return
		}
		corpusDBInfo, err := a.cncDB.LoadInfo(corpusID)
		if err != nil {
			api.WriteJSONErrorResponse(w, api.NewActionError("liveAttrs generator failed: '%s'", err), http.StatusInternalServerError)
			return
		}
		newConf, err := createLAConfig(
			a.conf,
			corpusInfo,
			corpusDBInfo,
			req.URL.Query().Get("atomStructure"),
			req.URL.Query().Get("bibIdAttr"),
			req.URL.Query()["mergeAttr"],
			req.URL.Query().Get("mergeFn"), // e.g. "identity", "intecorp"
		)
		if err != nil {
			api.WriteJSONErrorResponse(w, api.NewActionError("LiveAttrs generator failed: '%s'", err), http.StatusBadRequest)
			return
		}

		err = a.laConfCache.Save(newConf)
		if err != nil {
			api.WriteJSONErrorResponse(w, api.NewActionError("LiveAttrs generator failed: '%s'", err), http.StatusBadRequest)
			return
		}

		conf, err = a.laConfCache.Get(corpusID)
		if err != nil {
			api.WriteJSONErrorResponse(w, api.NewActionError("LiveAttrs generator failed: '%s'", err), http.StatusBadRequest)
			return
		}
	}

	if len(jsonArgs.VerticalFiles) > 0 {
		conf.VerticalFiles = jsonArgs.VerticalFiles
	}
	// TODO search collisions only in liveattrs type jobs

	jobID, err := uuid.NewUUID()
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionError("Failed to start liveattrs job for '%s'", corpusID), http.StatusUnauthorized)
		return
	}

	if prevRunning, ok := a.jobActions.LastUnfinishedJobOfType(corpusID, jobType); ok {
		api.WriteJSONErrorResponse(
			w,
			api.NewActionError(
				"LiveAttrs generator failed - the previous job '%s' have not finished yet",
				prevRunning.GetID(),
			),
			http.StatusConflict,
		)
		return
	}

	append := req.URL.Query().Get("append")
	status := &LiveAttrsJobInfo{
		ID:       jobID.String(),
		CorpusID: corpusID,
		Start:    jobs.CurrentDatetime(),
		Args: jobInfoArgs{
			VteConf: *conf,
			Append:  append == "1",
		},
	}
	err = a.createDataFromJobStatus(status)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	api.WriteJSONResponseWithStatus(w, http.StatusCreated, status)
}

// createDataFromJobStatus starts data extraction and generation
// based on (initial) job status
func (a *Actions) createDataFromJobStatus(status *LiveAttrsJobInfo) error {
	a.vteExitEvents[status.ID] = make(chan os.Signal)
	procStatus, err := vteLib.ExtractData(
		&status.Args.VteConf, status.Args.Append, a.vteExitEvents[status.ID])
	if err != nil {
		return fmt.Errorf("failed to start vert-tagextract: %s", err)
	}
	go func() {
		updateJobChan := a.jobActions.AddJobInfo(status)
		defer func() {
			close(updateJobChan)
			close(a.vteExitEvents[status.ID])
			delete(a.vteExitEvents, status.ID)
		}()

		for upd := range procStatus {
			updateJobChan <- &LiveAttrsJobInfo{
				ID:             status.ID,
				Type:           jobType,
				CorpusID:       status.CorpusID,
				Start:          status.Start,
				Update:         jobs.CurrentDatetime(),
				Error:          jobs.NewJSONError(upd.Error),
				ProcessedAtoms: upd.ProcessedAtoms,
				ProcessedLines: upd.ProcessedLines,
				NumRestarts:    status.NumRestarts,
				Args:           status.Args,
			}
			if upd.Error != nil {
				log.Print("ERROR: live attributes extraction failed - ", upd.Error)
				return
			}
		}

		if status.Args.VteConf.DB.Type == "sqlite" {
			err := sqlite.InstallSqliteDatabase(
				status.Args.VteConf.Corpus,
				status.Args.VteConf.DB.Name,
				a.conf.CorporaSetup.TextTypesDbDirPath,
			)
			if err != nil {
				updateJobChan <- &LiveAttrsJobInfo{
					ID:          status.ID,
					Type:        jobType,
					CorpusID:    status.CorpusID,
					Start:       status.Start,
					Error:       jobs.NewJSONError(err),
					NumRestarts: status.NumRestarts,
					Args:        status.Args,
				}

			} else {
				resp, err := http.Post(a.conf.KontextSoftResetURL, "application/json", nil)
				if err != nil || resp.StatusCode < 400 {
					updateJobChan <- &LiveAttrsJobInfo{
						ID:          status.ID,
						Type:        jobType,
						CorpusID:    status.CorpusID,
						Start:       status.Start,
						Error:       jobs.NewJSONError(err),
						NumRestarts: status.NumRestarts,
						Args:        status.Args,
					}
				}
			}
		}
	}()
	return nil
}

// Delete removes all the live attributes data for a corpus
func (a *Actions) Delete(w http.ResponseWriter, req *http.Request) {
	// TODO
}

func (a *Actions) runStopJobListener() {
	for id := range a.jobStopChannel {
		if job, ok := a.jobActions.GetJob(id); ok {
			if tJob, ok2 := job.(*LiveAttrsJobInfo); ok2 {
				if stopChan, ok3 := a.vteExitEvents[tJob.ID]; ok3 {
					stopChan <- os.Interrupt
				}
			}
		}
	}
}

func (a *Actions) getAttrValues(
	corpusInfo *corpus.DBInfo, qry query.Payload) (*response.QueryAns, error) {

	laConf, err := a.laConfCache.Get(corpusInfo.Name) // set(self._get_subcorp_attrs(corpus))
	if err != nil {
		return nil, err
	}
	srchAttrs := collections.NewSet(cnf.GetSubcorpAttrs(laConf)...)
	expandAttrs := collections.NewSet[string]()
	bibLabel := db.ImportKey(corpusInfo.BibLabelAttr)
	if bibLabel != "" {
		srchAttrs.Add(bibLabel)
	}
	// if in autocomplete mode then always expand list of the target column
	if qry.AutocompleteAttr != "" {
		a := db.ImportKey(qry.AutocompleteAttr)
		srchAttrs.Add(a)
		expandAttrs.Add(a)
		acVals, err := qry.Attrs.GetListingOf(qry.AutocompleteAttr)
		if err != nil {
			return nil, err
		}
		qry.Attrs[qry.AutocompleteAttr] = fmt.Sprintf("%%%s%%", acVals[0])
	}
	// also make sure that range attributes are expanded to full lists
	for attr := range qry.Attrs {
		if qry.Attrs.AttrIsRange(attr) {
			expandAttrs.Add(db.ImportKey(attr))
		}
	}
	qBuilder := &qbuilder.Builder{
		CorpusInfo:          corpusInfo,
		AttrMap:             qry.Attrs,
		SearchAttrs:         srchAttrs.ToOrderedSlice(),
		AlignedCorpora:      qry.Aligned,
		AutocompleteAttr:    qry.AutocompleteAttr,
		EmptyValPlaceholder: emptyValuePlaceholder,
	}
	dataIterator := qbuilder.DataIterator{
		DB:      a.laDB,
		Builder: qBuilder,
	}

	ans := response.QueryAns{
		Poscount:   0,
		AttrValues: make(map[string]any),
	}
	for _, sattr := range qBuilder.SearchAttrs {
		ans.AttrValues[sattr] = make([]*response.ListedValue, 0, 100)
	}
	// 1) values collected one by one are collected in tmp_ans and then moved to 'ans'
	//    with some exporting tweaks
	// 2) in case of values exceeding max. allowed list size we just accumulate their size
	//    directly to ans[attr]
	// {attr_id: {attr_val: num_positions,...},...}
	tmpAns := make(map[string]map[string]*response.ListedValue)
	bibID := db.ImportKey(qBuilder.CorpusInfo.BibIDAttr)
	err = dataIterator.Iterate(func(row qbuilder.ResultRow) error {
		ans.Poscount += row.Poscount
		for dbKey, dbVal := range row.Attrs {
			colKey := db.ExportKey(dbKey)
			switch tColVal := ans.AttrValues[colKey].(type) {
			case []*response.ListedValue:
				var valIdent string
				if colKey == corpusInfo.BibLabelAttr {
					valIdent = row.Attrs[bibID]

				} else {
					valIdent = row.Attrs[dbKey]
				}
				attrVal := response.ListedValue{
					ID:         valIdent,
					ShortLabel: shortenVal(dbVal, shortLabelMaxLength),
					Label:      dbVal,
					Grouping:   1,
				}
				_, ok := tmpAns[colKey]
				if !ok {
					tmpAns[colKey] = make(map[string]*response.ListedValue)
				}
				currAttrVal, ok := tmpAns[colKey][attrVal.ID]
				if ok {
					currAttrVal.Count += row.Poscount

				} else {
					attrVal.Count = row.Poscount
					tmpAns[colKey][attrVal.ID] = &attrVal
				}
			case int:
				ans.AttrValues[colKey] = tColVal + row.Poscount
			default:
				return fmt.Errorf(
					"invalid value type for attr %s for data iterator: %s",
					colKey, reflect.TypeOf(ans.AttrValues[colKey]),
				)
			}
		}
		return nil
	})
	if err != nil {
		return &ans, err
	}
	for attr, v := range tmpAns {
		for _, c := range v {
			ans.AddListedValue(attr, c)
		}
	}
	// now each line contains: (shortened_label, identifier, label, num_grouped_items, num_positions)
	// where num_grouped_items is initialized to 1
	if corpusInfo.BibGroupDuplicates > 0 {
		groupBibItems(&ans, bibLabel)
	}
	maxAttrListSize := qry.MaxAttrListSize
	if maxAttrListSize == 0 {
		maxAttrListSize = dfltMaxAttrListSize
	}
	response.ExportAttrValues(
		&ans,
		qBuilder.AlignedCorpora,
		expandAttrs.ToOrderedSlice(),
		corpusInfo.Locale,
		maxAttrListSize,
	)
	return &ans, nil
}

func (a *Actions) Query(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]

	var qry query.Payload
	err := json.NewDecoder(req.Body).Decode(&qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusBadRequest)
		return
	}
	corpInfo, err := a.cncDB.LoadInfo(corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	ans := a.eqCache.Get(corpusID, qry)
	if ans != nil {
		api.WriteJSONResponse(w, &ans)
		return
	}
	ans, err = a.getAttrValues(corpInfo, qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	a.usageData <- db.RequestData{CorpusID: corpusID, Payload: qry}
	a.eqCache.Set(corpusID, qry, ans)
	api.WriteJSONResponse(w, &ans)
}

func (a *Actions) FillAttrs(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]

	var qry fillattrs.Payload
	err := json.NewDecoder(req.Body).Decode(&qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	corpusDBInfo, err := a.cncDB.LoadInfo(corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionError("failed to fill attrs: '%s'", err), http.StatusInternalServerError)
		return
	}
	ans, err := db.FillAttrs(a.laDB, corpusDBInfo, qry)
	if err == db.ErrorEmptyResult {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusNotFound)
		return

	} else if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	api.WriteJSONResponse(w, &ans)
}

func (a *Actions) GetAdhocSubcSize(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]

	var qry equery.Payload
	err := json.NewDecoder(req.Body).Decode(&qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	corpora := append([]string{corpusID}, qry.Aligned...)
	corpusDBInfo, err := a.cncDB.LoadInfo(corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionError("failed to fill attrs: '%s'", err), http.StatusInternalServerError)
		return
	}
	size, err := db.GetSubcSize(a.laDB, corpusDBInfo.GroupedName(), corpora, qry.Attrs)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	api.WriteJSONResponse(w, response.GetSubcSize{Total: size})
}

func (a *Actions) GetBibliography(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]

	var qry biblio.Payload
	err := json.NewDecoder(req.Body).Decode(&qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusBadRequest)
		return
	}
	corpInfo, err := a.cncDB.LoadInfo(corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	laConf, err := a.laConfCache.Get(corpInfo.Name)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	ans, err := db.GetBibliography(a.laDB, corpInfo, laConf, qry)
	if err == db.ErrorEmptyResult {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusNotFound)
		return

	} else if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	api.WriteJSONResponse(w, &ans)
}

func (a *Actions) FindBibTitles(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]

	var qry biblio.PayloadList
	err := json.NewDecoder(req.Body).Decode(&qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusBadRequest)
		return
	}
	corpInfo, err := a.cncDB.LoadInfo(corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	laConf, err := a.laConfCache.Get(corpInfo.Name)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	ans, err := db.FindBibTitles(a.laDB, corpInfo, laConf, qry)
	if err == db.ErrorEmptyResult {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusNotFound)
		return

	} else if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	api.WriteJSONResponse(w, &ans)
}

func (a *Actions) AttrValAutocomplete(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]

	var qry query.Payload
	err := json.NewDecoder(req.Body).Decode(&qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusBadRequest)
		return
	}
	corpInfo, err := a.cncDB.LoadInfo(corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	ans, err := a.getAttrValues(corpInfo, qry)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	api.WriteJSONResponse(w, &ans)
}

func (a *Actions) Stats(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]
	ans, err := db.LoadUsage(a.laDB, corpusID)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusInternalServerError)
		return
	}
	api.WriteJSONResponse(w, &ans)
}

func (a *Actions) updateIndexesFromJobStatus(status *IdxUpdateJobInfo) {
	go func() {
		updateJobChan := a.jobActions.AddJobInfo(status)
		defer close(updateJobChan)
		finalStatus := *status
		corpusDBInfo, err := a.cncDB.LoadInfo(status.CorpusID)
		if err != nil {
			finalStatus.Error = jobs.NewJSONError(err)
		}
		ans := db.UpdateIndexes(a.laDB, corpusDBInfo, status.Args.MaxColumns)
		if ans.Error != nil {
			finalStatus.Error = jobs.NewJSONError(ans.Error)
		}
		finalStatus.Update = jobs.CurrentDatetime()
		finalStatus.Finished = true
		finalStatus.Result.RemovedIndexes = ans.RemovedIndexes
		finalStatus.Result.UsedIndexes = ans.UsedIndexes
		updateJobChan <- &finalStatus
	}()
}

func (a *Actions) UpdateIndexes(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	corpusID := vars["corpusId"]
	maxColumnsArg := req.URL.Query().Get("maxColumns")
	if maxColumnsArg == "" {
		api.WriteJSONErrorResponse(
			w, api.NewActionError("missing maxColumns argument"), http.StatusBadRequest)
		return
	}
	maxColumns, err := strconv.Atoi(maxColumnsArg)
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionErrorFrom(err), http.StatusUnprocessableEntity)
		return
	}
	jobID, err := uuid.NewUUID()
	if err != nil {
		api.WriteJSONErrorResponse(w, api.NewActionError("Failed to start 'update indexes' job for '%s'", corpusID), http.StatusUnauthorized)
		return
	}
	newStatus := IdxUpdateJobInfo{
		ID:       jobID.String(),
		Type:     "liveattrs-idx-update",
		CorpusID: corpusID,
		Start:    jobs.CurrentDatetime(),
		Update:   jobs.CurrentDatetime(),
		Finished: false,
		Args:     idxJobInfoArgs{MaxColumns: maxColumns},
	}
	a.updateIndexesFromJobStatus(&newStatus)
	api.WriteJSONResponseWithStatus(w, http.StatusCreated, &newStatus)
}

func (a *Actions) RestartLiveAttrsJob(jinfo *LiveAttrsJobInfo) error {
	if jinfo.NumRestarts >= a.conf.Jobs.MaxNumRestarts {
		return fmt.Errorf("cannot restart job %s - max. num. of restarts reached", jinfo.ID)
	}
	jinfo.Start = jobs.CurrentDatetime()
	jinfo.NumRestarts++
	jinfo.Update = jobs.CurrentDatetime()
	err := a.createDataFromJobStatus(jinfo)
	if err != nil {
		return err
	}
	log.Printf("Restarted liveAttributes job %s", jinfo.ID)
	return nil
}

func (a *Actions) RestartIdxUpdateJob(jinfo *IdxUpdateJobInfo) error {
	return nil
}

// NewActions is the default factory for Actions
func NewActions(
	conf *cnf.Conf,
	exitEvent <-chan os.Signal,
	jobStopChannel <-chan string,
	jobActions *jobs.Actions,
	cncDB *cncdb.CNCMySQLHandler,
	laDB *sql.DB,
	version cnf.VersionInfo,
) *Actions {
	usageChan := make(chan db.RequestData)
	vteExitEvents := make(map[string]chan os.Signal)
	go func() {
		for v := range exitEvent {
			for _, ch := range vteExitEvents {
				ch <- v
			}
		}
	}()
	actions := &Actions{
		exitEvent:      exitEvent,
		vteExitEvents:  vteExitEvents,
		conf:           conf,
		jobActions:     jobActions,
		jobStopChannel: jobStopChannel,
		laConfCache: cnf.NewLiveAttrsBuildConfLoader(
			conf.LiveAttrs.ConfDirPath,
			conf.LiveAttrs.DB,
		),
		cncDB:           cncDB,
		laDB:            laDB,
		eqCache:         cache.NewEmptyQueryCache(),
		structAttrStats: db.NewStructAttrUsage(laDB, usageChan),
		usageData:       usageChan,
	}
	go actions.structAttrStats.RunHandler()
	go actions.runStopJobListener()
	return actions
}
