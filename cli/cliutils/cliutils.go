package cliutils

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/open-horizon/anax/api"
	"github.com/open-horizon/anax/exchange"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"
	"regexp"
	"strconv"
)

const (
	HZN_API             = "http://localhost"
	AGBOT_HZN_API       = "http://localhost:8046"
	JSON_INDENT         = "  "
	MUST_REGISTER_FIRST = "this command can not be run before running 'hzn register'"

	// Exit Codes
	CLI_INPUT_ERROR    = 1 // we actually don't have control over the usage exit code that kingpin returns, so use the same code for input errors we catch ourselves
	JSON_PARSING_ERROR = 3
	READ_FILE_ERROR    = 4
	HTTP_ERROR         = 5
	//EXEC_CMD_ERROR = 6
	CLI_GENERAL_ERROR = 7
	NOT_FOUND = 8
	SIGNATURE_INVALID = 9
	INTERNAL_ERROR = 99

	// Anax API HTTP Codes
	ANAX_ALREADY_CONFIGURED = 409
	ANAX_NOT_CONFIGURED_YET = 424
)

// Holds the cmd line flags that were set so other pkgs can access
type GlobalOptions struct {
	Verbose *bool
}

var Opts GlobalOptions

type UserExchangeReq struct {
	Password string `json:"password"`
	Admin    bool   `json:"admin"`
	Email    string `json:"email"`
}

func Verbose(msg string, args ...interface{}) {
	if !*Opts.Verbose {
		return
	}
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprintf(os.Stderr, "[verbose] "+msg, args...) // send to stderr so it doesn't mess up stdout if they are piping that to jq or something like that
}

func Fatal(exitCode int, msg string, args ...interface{}) {
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprintf(os.Stderr, "Error: "+msg, args...)
	os.Exit(exitCode)
}

/*
func GetShortBinaryName() string {
	return path.Base(os.Args[0])
}
*/

// SplitIdToken splits an id:token or user:pw and return the parts.
func SplitIdToken(idToken string) (id, token string) {
	parts := strings.SplitN(idToken, ":", 2)
	id = parts[0] // SplitN will always at least return 1 element
	token = ""
	if len(parts) >= 2 {
		token = parts[1]
	}
	return
}

// OrgAndCreds prepends the org to creds (separated by /) unless creds already has an org prepended
func OrgAndCreds(org, creds string) string {
	if os.Getenv("USING_API_KEY") == "1" {
		return creds	// WIoTP API keys are globally unique and shouldn't be prepended with the org
	}
	id, _ := SplitIdToken(creds)	// only look for the / in the id, because the token is more likely to have special chars
	if strings.Contains(id, "/") {
		return creds	// already has the org at the beginning
	}
	return org+"/"+creds
}

// FormExchangeId combines url, version, arch the same way the exchange does to form the resource ID.
func FormExchangeId(url, version, arch string) string {
	// Remove the https:// from the beginning of workloadUrl and replace troublesome chars with a dash.
	//val workloadUrl2 = """^[A-Za-z0-9+.-]*?://""".r replaceFirstIn (url, "")
	//val workloadUrl3 = """[$!*,;/?@&~=%]""".r replaceAllIn (workloadUrl2, "-")     // I think possible chars in valid urls are: $_.+!*,;/?:@&~=%-
	//return OrgAndId(orgid, workloadUrl3 + "_" + version + "_" + arch).toString
	re := regexp.MustCompile(`^[A-Za-z0-9+.-]*?://`)
	url2 := re.ReplaceAllLiteralString(url, "")
	re = regexp.MustCompile(`[$!*,;/?@&~=%]`)
	url3 := re.ReplaceAllLiteralString(url2, "-")
	return url3 + "_" + version + "_" + arch
}

// ReadFile reads from a file or stdin, and returns it as a byte array.
func ReadFile(filePath string) []byte {
	var fileBytes []byte
	var err error
	if filePath == "-" {
		fileBytes, err = ioutil.ReadAll(os.Stdin)
	} else {
		fileBytes, err = ioutil.ReadFile(filePath)
	}
	if err != nil {
		Fatal(READ_FILE_ERROR, "reading %s failed: %v", filePath, err)
	}
	return fileBytes
}

// ReadJsonFile reads json from a file or stdin, eliminates comments, and returns it.
func ReadJsonFile(filePath string) []byte {
	var fileBytes []byte
	var err error
	if filePath == "-" {
		fileBytes, err = ioutil.ReadAll(os.Stdin)
	} else {
		fileBytes, err = ioutil.ReadFile(filePath)
	}
	if err != nil {
		Fatal(READ_FILE_ERROR, "reading %s failed: %v", filePath, err)
	}
	// remove /* */ comments
	re := regexp.MustCompile(`(?s)/\*.*?\*/`)
	newBytes := re.ReplaceAll(fileBytes, nil)
	return newBytes
}

// ConfirmRemove prompts the user to confirm they want to run the destructive cmd
func ConfirmRemove(question string) {
	// Prompt the user to make sure he/she wants to do this
	fmt.Print(question + " [y/N]: ")
	var response string
	fmt.Scanln(&response)
	if strings.TrimSpace(response) != "y" {
		fmt.Println("Exiting.")
		os.Exit(0)
	}
}

// GetHorizonUrlBase returns the base part of the horizon api url (which can be overridden by env var HORIZON_URL_BASE)
func GetHorizonUrlBase() string {
	envVar := os.Getenv("HORIZON_URL_BASE")
	if envVar != "" {
		return envVar
	}
	return HZN_API
}

// GetRespBodyAsString converts an http response body to a string
func GetRespBodyAsString(responseBody io.ReadCloser) string {
	buf := new(bytes.Buffer)
	buf.ReadFrom(responseBody)
	return buf.String()
}

func isGoodCode(actualHttpCode int, goodHttpCodes []int) bool {
	if len(goodHttpCodes) == 0 {
		return true // passing in an empty list of good codes means anything is ok
	}
	for _, code := range goodHttpCodes {
		if code == actualHttpCode {
			return true
		}
	}
	return false
}

func printHorizonRestError(apiMethod string, err error) {
	if os.Getenv("HORIZON_URL_BASE") == "" {
		Fatal(HTTP_ERROR, "Can't connect to the Horizon REST API to run %s. Run 'systemctl status horizon' to check if the Horizon agent is running. Or set HORIZON_URL_BASE to connect to another local port that is connected to a remote Horizon agent via a ssh tunnel. Specific error is: %v", apiMethod, err)
	} else {
		Fatal(HTTP_ERROR, "Can't connect to the Horizon REST API to run %s. Maybe the ssh tunnel associated with that port is down? Or maybe the remote Horizon agent at the other end of that tunnel is down. Specific error is: %v", apiMethod, err)
	}
}

// HorizonGet runs a GET on the anax api and fills in the specified structure with the json.
// If the list of goodHttpCodes is not empty and none match the actual http code, it will exit with an error. Otherwise the actual code is returned.
// Only if the actual code matches the 1st element in goodHttpCodes, will it parse the body into the specified structure.
func HorizonGet(urlSuffix string, goodHttpCodes []int, structure interface{}) (httpCode int) {
	url := GetHorizonUrlBase() + "/" + urlSuffix
	apiMsg := http.MethodGet + " " + url
	Verbose(apiMsg)
	resp, err := http.Get(url)
	if err != nil {
		printHorizonRestError(apiMsg, err)
	}
	defer resp.Body.Close()
	httpCode = resp.StatusCode
	Verbose("HTTP code: %d", httpCode)
	if !isGoodCode(httpCode, goodHttpCodes) {
		Fatal(HTTP_ERROR, "bad HTTP code from %s: %d", apiMsg, httpCode)
	}
	if httpCode == goodHttpCodes[0] {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			Fatal(HTTP_ERROR, "failed to read body response for %s: %v", apiMsg, err)
		}
		switch s := structure.(type) {
		case *string:
			// Just return the unprocessed response body
			*s = string(bodyBytes)
		default:
			// Put the response body in the specified struct
			err = json.Unmarshal(bodyBytes, structure)
			if err != nil {
				Fatal(JSON_PARSING_ERROR, "failed to unmarshal body response for %s: %v", apiMsg, err)
			}
		}
	}
	return
}

// HorizonDelete runs a DELETE on the anax api.
// If the list of goodHttpCodes is not empty and none match the actual http code, it will exit with an error. Otherwise the actual code is returned.
func HorizonDelete(urlSuffix string, goodHttpCodes []int) (httpCode int) {
	url := GetHorizonUrlBase() + "/" + urlSuffix
	apiMsg := http.MethodDelete + " " + url
	Verbose(apiMsg)
	httpClient := &http.Client{}
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		Fatal(HTTP_ERROR, "%s new request failed: %v", apiMsg, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		printHorizonRestError(apiMsg, err)
	}
	defer resp.Body.Close()
	httpCode = resp.StatusCode
	Verbose("HTTP code: %d", httpCode)
	if !isGoodCode(httpCode, goodHttpCodes) {
		Fatal(HTTP_ERROR, "bad HTTP code %d from %s: %s", httpCode, apiMsg, GetRespBodyAsString(resp.Body))
	}
	return
}

// HorizonPutPost runs a PUT or POST to the anax api to create or update a resource.
// If the list of goodHttpCodes is not empty and none match the actual http code, it will exit with an error. Otherwise the actual code is returned.
func HorizonPutPost(method string, urlSuffix string, goodHttpCodes []int, body interface{}) (httpCode int) {
	url := GetHorizonUrlBase() + "/" + urlSuffix
	apiMsg := method + " " + url
	Verbose(apiMsg)
	httpClient := &http.Client{}

	// Prepare body
	var jsonBytes []byte
	bodyIsBytes := false
	switch b := body.(type) {
	// If the body is a byte array or string, we treat it like a file being uploaded (not multi-part)
	case []byte:
		jsonBytes = b
		bodyIsBytes = true
	case string:
		jsonBytes = []byte(b)
		bodyIsBytes = true
	// Else it is a struct so assume it should be sent as json
	default:
		var err error
		jsonBytes, err = json.Marshal(body)
		if err != nil {
			Fatal(JSON_PARSING_ERROR, "failed to marshal body for %s: %v", apiMsg, err)
		}
	}
	requestBody := bytes.NewBuffer(jsonBytes)

	// Create the request and run it
	req, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		Fatal(HTTP_ERROR, "%s new request failed: %v", apiMsg, err)
	}
	req.Header.Add("Accept", "application/json")
	if bodyIsBytes {
		req.Header.Add("Content-Length", strconv.Itoa(len(jsonBytes)))
	} else {
		req.Header.Add("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		printHorizonRestError(apiMsg, err)
	}

	// Process the response
	defer resp.Body.Close()
	httpCode = resp.StatusCode
	Verbose("HTTP code: %d", httpCode)
	if !isGoodCode(httpCode, goodHttpCodes) {
		Fatal(HTTP_ERROR, "bad HTTP code %d from %s: %s", httpCode, apiMsg, GetRespBodyAsString(resp.Body))
	}
	return
}

// GetExchangeUrl returns the exchange url from the env var or anax api
func GetExchangeUrl() string {
	exchUrl := os.Getenv("HORIZON_EXCHANGE_URL_BASE")
	if exchUrl == "" {
		// Get it from anax
		status := api.Info{}
		HorizonGet("status", []int{200}, &status)
		exchUrl = status.Configuration.ExchangeAPI
	}
	exchUrl = strings.TrimSuffix(exchUrl, "/")	// anax puts a trailing slash on it
	if os.Getenv("USING_API_KEY") == "1" {
		re := regexp.MustCompile(`edgenode$`)
		exchUrl = re.ReplaceAllLiteralString(exchUrl, "edge")
	}
	return exchUrl
}

func printHorizonExchRestError(apiMethod string, err error) {
	if os.Getenv("HORIZON_EXCHANGE_URL_BASE") == "" {
		Fatal(HTTP_ERROR, "Can't connect to the Horizon Exchange REST API to run %s. Set HORIZON_EXCHANGE_URL_BASE to use an Exchange other than the one the Horizon Agent is currently configured for. Specific error is: %v", apiMethod, err)
	} else {
		Fatal(HTTP_ERROR, "Can't connect to the Horizon Exchange REST API to run %s. Maybe HORIZON_EXCHANGE_URL_BASE is set incorrectly? Or unset HORIZON_EXCHANGE_URL_BASE to use the Exchange that the Horizon Agent is configured for. Specific error is: %v", apiMethod, err)
	}
}

// ExchangeGet runs a GET to the exchange api and fills in the specified json structure. If the structure is just a string, fill in the raw json.
// If the list of goodHttpCodes is not empty and none match the actual http code, it will exit with an error. Otherwise the actual code is returned.
func ExchangeGet(urlBase string, urlSuffix string, credentials string, goodHttpCodes []int, structure interface{}) (httpCode int) {
	url := urlBase + "/" + urlSuffix
	apiMsg := http.MethodGet + " " + url
	Verbose(apiMsg)
	httpClient := &http.Client{}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		Fatal(HTTP_ERROR, "%s new request failed: %v", apiMsg, err)
	}
	req.Header.Add("Accept", "application/json")
	//req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("Basic %v", base64.StdEncoding.EncodeToString([]byte(credentials))))
	resp, err := httpClient.Do(req)
	if err != nil {
		printHorizonExchRestError(apiMsg, err)
	}
	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		Fatal(HTTP_ERROR, "failed to read body response for %s: %v", apiMsg, err)
	}
	httpCode = resp.StatusCode
	Verbose("HTTP code: %d", httpCode)
	if !isGoodCode(httpCode, goodHttpCodes) {
		Fatal(HTTP_ERROR, "bad HTTP code %d from %s, output: %s", httpCode, apiMsg, string(bodyBytes))
	}

	switch s := structure.(type) {
	case *string:
		// If the structure to fill in is just a string, unmarshal/remarshal it to get it in json indented form, and then return as a string
		//todo: this gets it in json indented form, but also returns the fields in random order (because they were interpreted as a map)
		var jsonStruct interface{}
		err = json.Unmarshal(bodyBytes, &jsonStruct)
		if err != nil {
			Fatal(JSON_PARSING_ERROR, "failed to unmarshal body response for %s: %v", apiMsg, err)
		}
		jsonBytes, err := json.MarshalIndent(jsonStruct, "", JSON_INDENT)
		if err != nil {
			Fatal(JSON_PARSING_ERROR, "failed to marshal exchange output: %v", err)
		}
		*s = string(jsonBytes)
	default:
		err = json.Unmarshal(bodyBytes, structure)
		if err != nil {
			Fatal(JSON_PARSING_ERROR, "failed to unmarshal body response for %s: %v", apiMsg, err)
		}
	}
	return
}

// ExchangePutPost runs a PUT or POST to the exchange api to create of update a resource. If body is a string, it will be given to the exchange
// as json. Otherwise the struct will be marshaled to json.
// If the list of goodHttpCodes is not empty and none match the actual http code, it will exit with an error. Otherwise the actual code is returned.
func ExchangePutPost(method string, urlBase string, urlSuffix string, credentials string, goodHttpCodes []int, body interface{}) (httpCode int) {
	url := urlBase + "/" + urlSuffix
	apiMsg := method + " " + url
	Verbose(apiMsg)
	httpClient := &http.Client{}
	var jsonBytes []byte
	switch b := body.(type) {
	case string:
		jsonBytes = []byte(b)
	default:
		var err error
		jsonBytes, err = json.Marshal(body)
		if err != nil {
			Fatal(JSON_PARSING_ERROR, "failed to marshal body for %s: %v", apiMsg, err)
		}
	}
	requestBody := bytes.NewBuffer(jsonBytes)
	req, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		Fatal(HTTP_ERROR, "%s new request failed: %v", apiMsg, err)
	}
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")
	if credentials != "" {
		req.Header.Add("Authorization", fmt.Sprintf("Basic %v", base64.StdEncoding.EncodeToString([]byte(credentials))))
	} // else it is an anonymous call
	resp, err := httpClient.Do(req)
	if err != nil {
		printHorizonExchRestError(apiMsg, err)
	}
	defer resp.Body.Close()
	httpCode = resp.StatusCode
	Verbose("HTTP code: %d", httpCode)
	if !isGoodCode(httpCode, goodHttpCodes) {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			Fatal(HTTP_ERROR, "failed to read body response for %s: %v", apiMsg, err)
		}
		respMsg := exchange.PostDeviceResponse{}
		err = json.Unmarshal(bodyBytes, &respMsg)
		if err != nil {
			Fatal(JSON_PARSING_ERROR, "failed to unmarshal body response for %s: %v", apiMsg, err)
		}
		Fatal(HTTP_ERROR, "bad HTTP code %d from %s: %s, %s", httpCode, apiMsg, respMsg.Code, respMsg.Msg)
	}
	return
}

// ExchangeDelete deletes a resource via the exchange api.
// If the list of goodHttpCodes is not empty and none match the actual http code, it will exit with an error. Otherwise the actual code is returned.
func ExchangeDelete(urlBase string, urlSuffix string, credentials string, goodHttpCodes []int) (httpCode int) {
	url := urlBase + "/" + urlSuffix
	apiMsg := http.MethodDelete + " " + url
	Verbose(apiMsg)
	httpClient := &http.Client{}
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		Fatal(HTTP_ERROR, "%s new request failed: %v", apiMsg, err)
	}
	req.Header.Add("Authorization", fmt.Sprintf("Basic %v", base64.StdEncoding.EncodeToString([]byte(credentials))))
	resp, err := httpClient.Do(req)
	if err != nil {
		printHorizonExchRestError(apiMsg, err)
	}
	// delete never returns a body
	httpCode = resp.StatusCode
	Verbose("HTTP code: %d", httpCode)
	if !isGoodCode(httpCode, goodHttpCodes) {
		Fatal(HTTP_ERROR, "bad HTTP code %d from %s", httpCode, apiMsg)
	}
	return
}

func ConvertTime(unixSeconds uint64) string {
	if unixSeconds == 0 {
		return ""
	}
	return time.Unix(int64(unixSeconds), 0).String()
}

/* Do not need at the moment, but keeping for reference...
// Run a command with optional stdin and args, and return stdout, stderr
func RunCmd(stdinBytes []byte, commandString string, args ...string) ([]byte, []byte) {
	// For debug, build the full cmd string
	cmdStr := commandString
	for _, a := range args {
		cmdStr += " " + a
	}
	if stdinBytes != nil { cmdStr += " < stdin" }
	Verbose("running: %v\n", cmdStr)

	// Create the command object with its args
	cmd := exec.Command(commandString, args...)
	if cmd == nil { Fatal(EXEC_CMD_ERROR, "did not get a command object") }

	var stdin io.WriteCloser
	//var jInbytes []byte
	var err error
	if stdinBytes != nil {
		// Create the std in pipe
		stdin, err = cmd.StdinPipe()
		if err != nil { Fatal(EXEC_CMD_ERROR, "Could not get Stdin pipe, error: %v", err) }
		// Read the input file
		//jInbytes, err = ioutil.ReadFile(stdinFilename)
		//if err != nil { Fatal(EXEC_CMD_ERROR,"Unable to read " + stdinFilename + " file, error: %v", err) }
	}
	// Create the stdout pipe to hold the output from the command
	stdout, err := cmd.StdoutPipe()
	if err != nil { Fatal(EXEC_CMD_ERROR,"could not retrieve output from command, error: %v", err) }
	// Create the stderr pipe to hold the errors from the command
	stderr, err := cmd.StderrPipe()
	if err != nil { Fatal(EXEC_CMD_ERROR,"could not retrieve stderr from command, error: %v", err) }

	// Start the command, which will block for input from stdin if the cmd reads from it
	err = cmd.Start()
	if err != nil { Fatal(EXEC_CMD_ERROR,"Unable to start command, error: %v", err) }

	if stdinBytes != nil {
		// Send in the std in bytes
		_, err = stdin.Write(stdinBytes)
		if err != nil { Fatal(EXEC_CMD_ERROR, "Unable to write to stdin of command, error: %v", err) }
		// Close std in so that the command will begin to execute
		err = stdin.Close()
		if err != nil { Fatal(EXEC_CMD_ERROR, "Unable to close stdin, error: %v", err) }
	}

	err = error(nil)
	// Read the output from stdout and stderr into byte arrays
	// stdoutBytes, err := readPipe(stdout)
	stdoutBytes, err := ioutil.ReadAll(stdout)
	if err != nil { Fatal(EXEC_CMD_ERROR,"could not read stdout, error: %v", err) }
	// stderrBytes, err := readPipe(stderr)
	stderrBytes, err := ioutil.ReadAll(stderr)
	if err != nil { Fatal(EXEC_CMD_ERROR,"could not read stderr, error: %v", err) }

	// Now block waiting for the command to complete
	err = cmd.Wait()
	if err != nil { Fatal(EXEC_CMD_ERROR, "command failed: %v, stderr: %s", err, string(stderrBytes)) }

	return stdoutBytes, stderrBytes
}
*/

/* Will probably need this....
func getString(v interface{}) string {
	if reflect.ValueOf(v).IsNil() { return "" }
	return fmt.Sprintf("%v", reflect.Indirect(reflect.ValueOf(v)))
}
*/
