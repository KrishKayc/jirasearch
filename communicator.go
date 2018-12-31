package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Communicator represents REST calls over network
type Communicator interface {
	// CreateRequestAndGetResponse creates http request and gives back the response body
	CreateRequestAndGetResponse(apiPath string, params map[string]string) []byte
}

// JiraCommunicator represent API calls to Jira
type JiraCommunicator struct {
	Url       string
	AuthToken string
}

// CreateRequestAndGetResponse creates JIRA request and gives back the response body
func (jc *JiraCommunicator) CreateRequestAndGetResponse(apiPath string, params map[string]string) []byte {
	req := CreateRequest(jc.Url, apiPath, jc.AuthToken, params)
	client := &http.Client{}
	resp, err := client.Do(req)
	HandleError(err)

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	HandleError(err)

	return body
}

// CreateRequest creates http request for the jiraUrl from config and path passed
func CreateRequest(jiraUrl string, apiPath string, authToken string, params map[string]string) *http.Request {
	var finalPath string
	bearer := "Basic " + authToken
	if params != nil {
		var endPoint *url.URL
		endPoint, err := url.Parse(jiraUrl)
		HandleError(err)

		endPoint.Path += apiPath
		parameters := url.Values{}

		for k, v := range params {
			parameters.Add(k, v)
		}

		endPoint.RawQuery = parameters.Encode()
		finalPath = endPoint.String()

	} else {
		finalPath = jiraUrl + apiPath
	}

	req, err := http.NewRequest("GET", finalPath, nil)
	req.Header.Add("Authorization", bearer)
	HandleError(err)

	return req

}

// CreateRequestAndGetResponse creates http request for the jiraUrl from config and path passed and gets the response body
func CreateRequestAndGetResponse(jiraUrl string, apiPath string, authToken string, params map[string]string) []byte {

	req := CreateRequest(jiraUrl, apiPath, authToken, params)
	client := &http.Client{}
	resp, err := client.Do(req)
	HandleError(err)

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	HandleError(err)

	return body
}

// GetCustomFields gets all the custom fields for the jiraUrl mentioned in the config
func GetCustomFields(config Configuration, customFieldChannel chan map[string]string, communicator Communicator) {

	body := communicator.CreateRequestAndGetResponse("/rest/api/2/field", nil)
	//body := CreateRequestAndGetResponse(config.JiraUrl, "/rest/api/2/field", config.AuthToken, nil)
	var fields []map[string]interface{}
	json.Unmarshal([]byte(body), &fields)

	var result map[string]string
	result = make(map[string]string)
	staticFields := make(map[string]string, 0)

	for _, field := range fields {
		if field["custom"].(bool) {
			_, ok := result[field["name"].(string)]
			if !ok {
				_, isStaticField := staticFields[strings.ToLower(field["name"].(string))]
				if !isStaticField {
					result[strings.ToLower(field["name"].(string))] = strings.ToLower(field["id"].(string))
				}
			}
		} else {
			_, ok := result[strings.ToLower(field["name"].(string))]
			if ok {
				delete(result, strings.ToLower(field["name"].(string)))
			} else {
				staticFields[strings.ToLower(field["name"].(string))] = strings.ToLower(field["id"].(string))
			}
		}
	}

	customFieldChannel <- result
}

// SearchIssues finds issues based on the jql passed
func SearchIssues(config Configuration, jql string, processedFields []string, issueRetrievedChannel chan JiraIssue, communicator Communicator) {

	params := make(map[string]string, 0)
	params["jql"] = jql
	params["fields"] = strings.Join(processedFields, ",")
	params["maxResults"] = "1000"

	body := communicator.CreateRequestAndGetResponse("/rest/api/2/search", params)
	//body := CreateRequestAndGetResponse(config.JiraUrl, "/rest/api/2/search", config.AuthToken, params)
	var responseResult map[string]interface{}
	var issues []interface{}
	json.Unmarshal([]byte(body), &responseResult)

	issues = responseResult["issues"].([]interface{})

	for _, issue := range issues {
		jiraIssue := JiraIssue{Data: issue.(map[string]interface{}), Fields: processedFields}
		issueRetrievedChannel <- jiraIssue
	}

}

// GetIssue fetches Issue based from the jiraUrl in the config and issueId passed
func GetIssue(config Configuration, issueId string, includeChangeLog bool, communicator Communicator) map[string]interface{} {

	var getIssueUrl string

	if includeChangeLog {
		getIssueUrl = "/rest/api/2/issue/" + issueId + "?expand=changelog"
	} else {
		getIssueUrl = "/rest/api/2/issue/" + issueId
	}

	body := communicator.CreateRequestAndGetResponse(getIssueUrl, nil)
	//body := CreateRequestAndGetResponse(config.JiraUrl, getIssueUrl, config.AuthToken, nil)

	var responseResult map[string]interface{}
	json.Unmarshal([]byte(body), &responseResult)

	return responseResult
}

// GetSubTasksForIssue gets All Sub Tasks for the passed issue
func GetSubTasksForIssue(config Configuration, issue JiraIssue, finalIssueChannel chan JiraIssue, includeChangeLog bool, totalRestCalls *int, communicator Communicator) {

	issueId := issue.Data["id"].(string)
	*totalRestCalls++
	parent := GetIssue(config, issueId, includeChangeLog, communicator)
	subTasks := parent["fields"].(map[string]interface{})["subtasks"].([]interface{})
	result := make([]SubTask, 0)

	for _, subTask := range subTasks {
		*totalRestCalls++
		subTaskIssue := GetIssue(config, subTask.(map[string]interface{})["id"].(string), false, communicator)
		assignee := GetValueFromField(subTaskIssue, "assignee")
		issueType := GetValueFromField(subTaskIssue, "issuetype")
		name := GetValueFromField(subTaskIssue, "summary")
		totalHours := GetValueFromField(subTaskIssue, "timetracking")
		currentSubTask := SubTask{Type: issueType, Name: name, AssigneeName: assignee, TotalHours: totalHours}

		result = append(result, currentSubTask)
	}

	issue.SubTasks = result

	parentIssueType := GetValueFromField(parent, "issuetype")
	if IsBug(parentIssueType) {
		issue.AssigneeName = GetDeveloperNameFromLog(parent)
	}

	finalIssueChannel <- issue

}

// IsBug determines whether the issue type is a bug
func IsBug(issueType string) bool {
	return strings.ToLower(issueType) == "bug" || strings.ToLower(issueType) == "functional bug" || strings.ToLower(issueType) == "production issue"
}

// GetDeveloperNameFromLog gets Developer Name From the work log record where status was 'In Development' stage
func GetDeveloperNameFromLog(issue map[string]interface{}) string {
	developerName := ""
	histories := issue["changelog"].(map[string]interface{})["histories"].([]interface{})
	for _, history := range histories {
		mapHistory := history.(map[string]interface{})
		items := mapHistory["items"].([]interface{})
		for _, item := range items {
			strInDevelopment, ok := item.(map[string]interface{})["toString"].(string)
			if ok && strInDevelopment == "In Development" {
				developerName = mapHistory["author"].(map[string]interface{})["displayName"].(string)
				break
			}
		}

		if developerName != "" {
			break
		}
	}

	return developerName

}

// GetValueFromField gets the value from the 'fields' property of the issue
func GetValueFromField(issue map[string]interface{}, field string) string {
	val, ok := issue["fields"]
	if ok {
		fieldsMap := val.(map[string]interface{})

		val, ok := fieldsMap[field]
		if ok {
			if strings.ToLower(field) == "created" {
				dateVal, _ := time.Parse("2006-01-02T15:04:05.999-0700", val.(string))
				return dateVal.Format("02/Jan/06")
			}
			return strings.Replace(GetValue(val, field), ",", "", -1)
		}
	}
	return "N/A"
}

// GetValue gets the value based on the type of interface
func GetValue(val interface{}, fieldName string) string {
	var result string
	arrayVal, isArray := val.([]interface{})
	mapVal, isMap := val.(map[string]interface{})
	if isArray {
		result = arrayVal[0].(map[string]interface{})["value"].(string)
	} else if isMap {
		tmpResult, ok := mapVal[GetNestedMapKeyName(fieldName)]
		if ok {
			result = tmpResult.(string)
		}
	} else if val != nil {
		result = fmt.Sprint(val)
	}

	return result
}

// GetNestedMapKeyName gets the nested field name to search for a parent name
func GetNestedMapKeyName(fieldName string) string {
	if strings.ToLower(fieldName) == "assignee" || strings.ToLower(fieldName) == "reporter" {
		return "displayName"
	}

	if strings.ToLower(fieldName) == "issuetype" || strings.ToLower(fieldName) == "status" || strings.ToLower(fieldName) == "priority" {
		return "name"
	}

	if strings.ToLower(fieldName) == "timetracking" {
		return "originalEstimate"
	}

	return "value"
}
