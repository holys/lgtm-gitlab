package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

var (
	privateToken = flag.String("private_token", "", "gitlab private token which used to accept merge request. can be found in https://your.gitlab.com/profile/account")
	gitlabURL    = flag.String("gitlab_url", "", "e.g. https://your.gitlab.com")
)

const (
	ValidLGTMCount = 2 // 满足条件的LGTM 数量
)

var (
	ErrInvalidRequest     = errors.New("invalid request body")
	ErrInvalidContentType = errors.New("invalid content type")
	RespOK                = []byte("OK")

	ObjectNote               = "note"
	NoteableTypeMergeRequest = "MergeRequest"
	NoteLGTM                 = "LGTM"
	StatusCanbeMerged        = "can_be_merged"
)

var (
	mutex sync.RWMutex
	// map[merge_request_id][count]
	lgtmCount = make(map[int]int)

	glURL *url.URL
)

func main() {
	flag.Parse()

	if *privateToken == "" {
		fmt.Println("private token is required")
		return
	}
	if *gitlabURL == "" {
		fmt.Println("gitlab url is required")
		return
	}

	parseURL(*gitlabURL)

	fmt.Println("start http server")
	http.HandleFunc("/gitlab/hook", LGTM)
	go func() {
		http.ListenAndServe(":8989", nil)
	}()

	<-(chan struct{})(nil)
}

func parseURL(urlStr string) {
	var err error
	glURL, err = url.Parse(urlStr)
	if err != nil {
		panic(err.Error())
	}
}

func LGTM(w http.ResponseWriter, r *http.Request) {
	log.Printf("method:%s, remote_addr:%s, form:%+v, header:%+v", r.Method, r.RemoteAddr, r.Form, r.Header)

	var errRet error
	defer func() {
		if errRet != nil {
			errMsg := fmt.Sprintf("error occurs:%s", errRet.Error())
			log.Println(errMsg)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, errMsg)
			return
		}
		w.Write(RespOK)
	}()

	if r.Header.Get("Content-Type") != "application/json" {
		errRet = ErrInvalidContentType
		return
	}
	if r.Body == nil {
		errRet = ErrInvalidRequest
		return
	}

	var comment Comment
	if err := json.NewDecoder(r.Body).Decode(&comment); err != nil {
		errRet = err
		return
	}

	checkLgtm(comment)
}

func checkLgtm(comment Comment) error {
	log.Printf("debug comment:%+v", comment)
	if comment.ObjectKind != ObjectNote {
		// unmatched, do nothing
		return nil
	}

	if comment.ObjectAttributes.NoteableType != NoteableTypeMergeRequest {
		// unmatched, do nothing
		return nil
	}

	if strings.ToUpper(comment.ObjectAttributes.Note) != NoteLGTM {
		// unmatched, do nothing
		return nil
	}

	// TODO: 检查评论LGTM的两个人 是不同的人

	var canbeMerged bool

	mutex.Lock()
	if count, ok := lgtmCount[comment.MergeRequest.ID]; ok {
		newCount := count + 1
		if newCount >= ValidLGTMCount {
			canbeMerged = true
		}
		lgtmCount[comment.MergeRequest.ID] = newCount
	} else {
		lgtmCount[comment.MergeRequest.ID] = 1
	}
	mutex.Unlock()

	log.Printf("counter: %+v", lgtmCount)

	if canbeMerged && comment.MergeRequest.MergeStatus == StatusCanbeMerged {
		log.Printf("The MR can be merged. ")
		acceptMergeRequest(comment.ProjectID, comment.MergeRequest.ID, comment.MergeRequest.MergeParams.ForceRemoveSourceBranch)
	}

	return nil
}

func acceptMergeRequest(projectID int, mergeRequestID int, shouldRemoveSourceBranch bool) {
	params := map[string]string{
		"should_remove_source_branch": "true",
	}
	bodyBytes, err := json.Marshal(params)
	if err != nil {
		log.Printf("json marshal error:%s", err.Error())
		return
	}

	glURL.Path = fmt.Sprintf("/api/v3/projects/%d/merge_requests/%d/merge", projectID, mergeRequestID)
	req, err := http.NewRequest("PUT", glURL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("http NewRequest error:%s", err.Error())
		return
	}
	req.Header.Set("Conntent-Type", "application/json")
	// authenticate
	req.Header.Set("PRIVATE-TOKEN", *privateToken) // my private token

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("execute request error:%s", err.Error())
		return
	}

	switch resp.StatusCode {
	// 200
	case http.StatusOK:
		log.Printf("accept merge request successfully.")
	// 405
	case http.StatusMethodNotAllowed:
		log.Printf("it has some conflicts and can not be merged")
	// 406
	case http.StatusNotAcceptable:
		log.Printf("merge request is already merged or closed")
	}
}

// Comment represents gitlab comment events
type Comment struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Name      string `json:"name"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
	ProjectID int `json:"project_id"`
	Project   struct {
		Name              string      `json:"name"`
		Description       string      `json:"description"`
		WebURL            string      `json:"web_url"`
		AvatarURL         interface{} `json:"avatar_url"`
		GitSSHURL         string      `json:"git_ssh_url"`
		GitHTTPURL        string      `json:"git_http_url"`
		Namespace         string      `json:"namespace"`
		VisibilityLevel   int         `json:"visibility_level"`
		PathWithNamespace string      `json:"path_with_namespace"`
		DefaultBranch     string      `json:"default_branch"`
		Homepage          string      `json:"homepage"`
		URL               string      `json:"url"`
		SSHURL            string      `json:"ssh_url"`
		HTTPURL           string      `json:"http_url"`
	} `json:"project"`
	ObjectAttributes struct {
		ID                   int         `json:"id"`
		Note                 string      `json:"note"`
		NoteableType         string      `json:"noteable_type"`
		AuthorID             int         `json:"author_id"`
		CreatedAt            string      `json:"created_at"`
		UpdatedAt            string      `json:"updated_at"`
		ProjectID            int         `json:"project_id"`
		Attachment           interface{} `json:"attachment"`
		LineCode             interface{} `json:"line_code"`
		CommitID             string      `json:"commit_id"`
		NoteableID           int         `json:"noteable_id"`
		StDiff               interface{} `json:"st_diff"`
		System               bool        `json:"system"`
		UpdatedByID          interface{} `json:"updated_by_id"`
		Type                 interface{} `json:"type"`
		Position             interface{} `json:"position"`
		OriginalPosition     interface{} `json:"original_position"`
		ResolvedAt           interface{} `json:"resolved_at"`
		ResolvedByID         interface{} `json:"resolved_by_id"`
		DiscussionID         string      `json:"discussion_id"`
		OriginalDiscussionID interface{} `json:"original_discussion_id"`
		URL                  string      `json:"url"`
	} `json:"object_attributes"`
	Repository struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Homepage    string `json:"homepage"`
	} `json:"repository"`
	MergeRequest struct {
		ID              int         `json:"id"`
		TargetBranch    string      `json:"target_branch"`
		SourceBranch    string      `json:"source_branch"`
		SourceProjectID int         `json:"source_project_id"`
		AuthorID        int         `json:"author_id"`
		AssigneeID      int         `json:"assignee_id"`
		Title           string      `json:"title"`
		CreatedAt       string      `json:"created_at"`
		UpdatedAt       string      `json:"updated_at"`
		MilestoneID     interface{} `json:"milestone_id"`
		State           string      `json:"state"`
		MergeStatus     string      `json:"merge_status"`
		TargetProjectID int         `json:"target_project_id"`
		Iid             int         `json:"iid"`
		Description     string      `json:"description"`
		Position        int         `json:"position"`
		LockedAt        interface{} `json:"locked_at"`
		UpdatedByID     interface{} `json:"updated_by_id"`
		MergeError      interface{} `json:"merge_error"`
		MergeParams     struct {
			ForceRemoveSourceBranch bool `json:"force_remove_source_branch"`
		} `json:"merge_params"`
		MergeWhenBuildSucceeds   bool        `json:"merge_when_build_succeeds"`
		MergeUserID              interface{} `json:"merge_user_id"`
		MergeCommitSha           interface{} `json:"merge_commit_sha"`
		DeletedAt                interface{} `json:"deleted_at"`
		InProgressMergeCommitSha interface{} `json:"in_progress_merge_commit_sha"`
		Source                   struct {
			Name              string `json:"name"`
			Description       string `json:"description"`
			WebURL            string `json:"web_url"`
			AvatarURL         string `json:"avatar_url"`
			GitSSHURL         string `json:"git_ssh_url"`
			GitHTTPURL        string `json:"git_http_url"`
			Namespace         string `json:"namespace"`
			VisibilityLevel   int    `json:"visibility_level"`
			PathWithNamespace string `json:"path_with_namespace"`
			DefaultBranch     string `json:"default_branch"`
			Homepage          string `json:"homepage"`
			URL               string `json:"url"`
			SSHURL            string `json:"ssh_url"`
			HTTPURL           string `json:"http_url"`
		} `json:"source"`
		Target struct {
			Name              string      `json:"name"`
			Description       string      `json:"description"`
			WebURL            string      `json:"web_url"`
			AvatarURL         interface{} `json:"avatar_url"`
			GitSSHURL         string      `json:"git_ssh_url"`
			GitHTTPURL        string      `json:"git_http_url"`
			Namespace         string      `json:"namespace"`
			VisibilityLevel   int         `json:"visibility_level"`
			PathWithNamespace string      `json:"path_with_namespace"`
			DefaultBranch     string      `json:"default_branch"`
			Homepage          string      `json:"homepage"`
			URL               string      `json:"url"`
			SSHURL            string      `json:"ssh_url"`
			HTTPURL           string      `json:"http_url"`
		} `json:"target"`
		LastCommit struct {
			ID        string    `json:"id"`
			Message   string    `json:"message"`
			Timestamp time.Time `json:"timestamp"`
			URL       string    `json:"url"`
			Author    struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		} `json:"last_commit"`
		WorkInProgress bool `json:"work_in_progress"`
	} `json:"merge_request"`
}

// 后续支持 redis. HINCR lgtm merge_id 1
