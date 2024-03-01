package main

import (
    "encoding/json"
    "fmt"
    "os"
    "log"
    "net/http"
    "bytes"
    "io/ioutil"
    "strconv"
    "strings"
    "math/big"
    "time"
)

// Networks struct holds an array of `Network` struct
type Networks struct {
    Network []Network `json:"networks"` // List of networks to be monitored
}

// Network struct defines properties for a network to be monitored
type Network struct {
    Name                string    `json:"name"` // Name of the network
    Type                string    `json:"type"` // Type of network, eg. ethereum, cosmos
    RpcUrl              string    `json:"rpcUrl"` // Rpc URL of the network
    Accounts            []Account `json:"accounts"` // Accounts associated with the network
    Numerator           float64   `json:"numerator"` // Numerator for converting balance
    Bad_request_threshold float64  `json:"bad_request_threshold"` // Bad request threshold for the network
    Denom               string    `json:"denom"` // Denomination of the network's currency
    Coingecko_name      string    `json:"coingecko_name"` // Name of the network on coingecko to calc price
}

// Account struct represents an account in a network
type Account struct {
    Address          string  `json:"address"` // Address of the account
    Purpose          string  `json:"purpose"` // Purpose of the account
    Balance_threshold float64 `json:"balance_threshold"` // Threshold balance for the account
    RPC_down         int // Number of consecutive failed RPC requests
    Down             bool // Flag indicating if the account's balance is below the threshold
}

// Responses struct holds cosmos.directory API responses
type Responses struct {
    Balances []Response     `json:"balances"` // List of balances in the responses
}

// Response struct represents a balance response
type Response struct {
    Denom string  `json:"denom"` // Denomination of the currency
    Amount string `json:"amount"` // Amount of the currency
}

// IP struct to hold the IP address from the query
type IP struct {
    Query string // Queried IP address
}

// EthRPCRequest struct represents an Ethereum RPC request
type EthRPCRequest struct {
    JSONRPC string        `json:"jsonrpc"` // JSONRPC version
    Method  string        `json:"method"` // Method for the request
    Params  []interface{} `json:"params"` // Parameters for the request
    ID      int           `json:"id"` // ID of the request
}

// EthRPCResponse struct represents an Ethereum RPC response
type EthRPCResponse struct {
    ID      int             `json:"id"` // ID of the response
    Result  string          `json:"result"` // Result of the request
    Error   *EthRPCError    `json:"error,omitempty"` // Error from the request, if any
}

// EthRPCError struct represents an error in an Ethereum RPC response
type EthRPCError struct {
    Code    int    `json:"code"` // Error code
    Message string `json:"message"` // Error message
}

// Slack webhook URLs for alerting
const (
    // Webhook for reporting wallet balances
    WALLET_SLACK_WEBHOOK string = ""
    // Webhook for reporting rpc status
    RPC_SLACK_WEBHOOK string = ""
    // Number of hours in a day, used for time-based computations
    DAY int = 24
)

// Globals for managing network check configurations
var (
    // Initial threshold value for RPC request failures
    THRESHOLD int = 0
    // Interval at which we check balances, in hours
    INTERVAL float64 = 0.5
)

// main function executes the network checks
func main() {
    networks := initialiseNetworks() // Initialise the networks from the JSON config file

    // Main loop for checking each network and account
    for {
        checkConfigUpdates(&networks) // Check for configuration updates

        var issues bool // Flag to track if any issues were encountered
        for k, v := range networks.Network {
            THRESHOLD = int(networks.Network[k].Bad_request_threshold/INTERVAL)
            for i, account := range v.Accounts {
                var responses *Responses
                var err error

                // Check the network type and query the balance accordingly
                if v.Type == "ethereum" {
                    responses, err = getEthereumBalance(v, account)
                } else if v.Type == "cosmos" {
                    responses, err = getCosmosBalance(v, account)
                }

                // If there's an error, log it, increment the error count and continue to the next account
                if err != nil {
                    // Increment the count of failed RPC calls
                    networks.Network[k].Accounts[i].RPC_down++
                    // Mark that we encountered issues
                    issues = true
                    // Log the error message
                    timeStamp(fmt.Sprintf("❌ Failed querying: %v/cosmos/bank/v1beta1/balances/%v ; error: %v ; consecutive bad run #: %v", v.RpcUrl, account.Address, err, networks.Network[k].Accounts[i].RPC_down))
                    check_rpc_health(networks.Network[k], networks.Network[k].Accounts[i])
                    continue
                }

                balance, err, resultGood := parseBalanceResponse(responses, v)
                if resultGood && !account.Down {
                    timeStamp(fmt.Sprintf("✅ Checked %v--%v and balance is good ; balance: `%.f`", networks.Network[k].Name, account.Purpose, balance/networks.Network[k].Numerator))
                }
                if err != nil {
                    networks.Network[k].Accounts[i].RPC_down++
                    issues = true
                    timeStamp(fmt.Sprintf("[NOTICE] GET %v/cosmos/bank/v1beta1/balances/%v but malformed json response; empty contents: body: unmarshaled: `%+v` ; consecutive bad run #: %v", v.RpcUrl, account.Address, responses, networks.Network[k].Accounts[i].RPC_down))
                    check_rpc_health(networks.Network[k], networks.Network[k].Accounts[i])
                    continue
                }
                if networks.Network[k].Accounts[i].RPC_down >= THRESHOLD {
                    alertRPC("good", v, account)
                    networks.Network[k].Accounts[i].RPC_down = 0
                }

                if balance < account.Balance_threshold {
                    if v.Denom == "wei" {
                        timeStamp(fmt.Sprintf("💔 [INFORMATIONAL] Wallet from %s - %s is low on funds. Balance: %.f %v / %.4f ETH", networks.Network[k].Name, account.Purpose, balance, v.Denom, balance/v.Numerator))
                    } else {
                        timeStamp(fmt.Sprintf("💔 [INFORMATIONAL] Wallet from %s - %s is low on funds. Balance: %.f %v / %.4f %v", networks.Network[k].Name, account.Purpose, balance, v.Denom, balance/v.Numerator, v.Denom[1:]))
                    }
                    if !account.Down {
                        alertBalance("bad", balance, v, account)
                        // Flag the network as having a low balance, so that we
                        // can 1) ignore it going forward and 2) catch it
                        // in the following condition when it gets refilled
                        networks.Network[k].Accounts[i].Down = true
                    }

                // If balance is > threshold, and the account was previously low
                // then remove the flag and alert slack that we're good now
                } else if balance > account.Balance_threshold && account.Down {
                    networks.Network[k].Accounts[i].Down = false
                    timeStamp(fmt.Sprintf("💚 [INFORMATIONAL] Wallet from %s is topped up again. New balance: %.f %v / %.2f %v", account.Purpose, balance, v.Denom, balance/v.Numerator, v.Denom[1:]))
                    alertBalance("good", balance, v, account)
                }
                // If we are here, then the RPC has responded successfully, and we should reset the counter
                networks.Network[k].Accounts[i].RPC_down = 0
            }
        }
        finishLog(issues)
        time.Sleep(time.Duration(60*INTERVAL)*time.Minute)
    }
}

// Checks if there exists an ovveride API address in networks.json
func populateUrl(network Network) string{
    // Use default if no override configured
    var url string
    if network.RpcUrl == "" {
        url = fmt.Sprintf("https://rest.cosmos.directory/%v", network.Name)
    } else {
        url = fmt.Sprintf("%v", network.RpcUrl)
    }
    return url
}

// pass pointer to existing struct of Networks
// and populate with values from config file
// and maintain existing up/down statistics so that
// we don't have to restart process on config changes
func checkConfigUpdates(current_networks *Networks) {
    placeholder_networks := initialiseNetworks()
    for k, _ := range placeholder_networks.Network {
        for i, _ := range placeholder_networks.Network[k].Accounts {
            placeholder_networks.Network[k].Accounts[i].Down = current_networks.Network[k].Accounts[i].Down
            placeholder_networks.Network[k].Accounts[i].RPC_down = current_networks.Network[k].Accounts[i].RPC_down
        }
    }
    *current_networks = placeholder_networks
}

func finishLog(issues bool) {
    if !issues {
        timeStamp(fmt.Sprintf("==== Run finished and all networks queried successfully ===="))
    } else {
        timeStamp(fmt.Sprintf("==== ❌ Run finished but experienced problems querying one or more networks  ===="))
    }
}

func check_rpc_health(network Network, account Account) {
    if account.RPC_down % THRESHOLD == 0 && account.RPC_down != 0  { // on every 2 consecutive days of failed requests, alert slack
        alertRPC("bad", network, account)
    }
}

// On each run we re-init the networks struct to check for config updates
func initialiseNetworks() Networks {
    networksFile, err := os.Open("networks.json")
    if err != nil {
        timeStamp(fmt.Sprintf("[ERROR] Failed to open networks.json; err: `%v`\n", err))
    } else {
        timeStamp("Opened networks.json\n")
    }

    // Defer the closing of file so we can parse it later on
    defer networksFile.Close()

    // Read bytestream, unmarshal the json into our Networks struct
    byteValue, err := ioutil.ReadAll(networksFile)
    if err != nil {
        timeStamp(fmt.Sprintf("[ERROR] Failed to read bytestream from networks.json; err: `%v`\n", err))
    }
    var networks Networks
    json.Unmarshal(byteValue, &networks)

    for k, v := range networks.Network {
        networks.Network[k].RpcUrl = populateUrl(v)
    }
    return networks
}

// send to slack webhook
func alertRPC(status string, network Network, account Account) {
    var msg string = "```\n"

    switch status {
    case "bad":
        msg = fmt.Sprintf("%v❌ REST ENDPOINT for *%v*: %v down for %v hours\n", msg, network.Name, network.RpcUrl, float64(account.RPC_down)*(INTERVAL/1))
    case "good":
        msg = fmt.Sprintf("%v✅ REST ENDPOINT for *%v*: %v back up after %v hours\n", msg, network.Name, network.RpcUrl, float64(account.RPC_down+1)*(INTERVAL/1)) //+1 because we don't increment the run where we alert back up
    }
    msg = fmt.Sprintf("%v⌛ Threshold for %v: %v hrs\n\n/Used with: %v | @%v\n```\n", msg, network.Name, network.Bad_request_threshold, account.Purpose, GetIp())

    // Prepare slack webhook msg
    values := map[string]string{"text": msg}
    json_data, err := json.Marshal(values)
    if err != nil {
        timeStamp(fmt.Sprintf("[NOTICE] Failed marshaling data: %v ; error: %v\n", values, err))
    }
    // Send slack webhook message
    _, err = http.Post(RPC_SLACK_WEBHOOK, "application/json", bytes.NewBuffer(json_data))
    if err != nil {
        timeStamp(fmt.Sprintf("[ALERT] Error: failed to send alert to slack: %v error: %v\n", RPC_SLACK_WEBHOOK, err))
    }
    fmt.Printf("---- Sent RPC `%v` alert to slack ----\n", status)
}


// send to slack webhook
func alertBalance(status string, balance float64, network Network, account Account) {
    normal_numeration := float64(balance)/float64(network.Numerator)
    current_value := getValue(network.Coingecko_name, normal_numeration)

    var msg string
    var denomLarge string
    if network.Denom == "wei" {
        denomLarge = "ETH"
    } else {
        denomLarge = network.Denom[1:]
    }
    switch status {
    case "bad":
        msg = fmt.Sprintf("⛽ *%v* needs topping up\n" +
                        "```\n" +
                        "====  Wallet  ====\n" +
                        "%v\n\n"+
                        "==== Balance ====\n" +
                        "%v: %.f\n" +
                        "%v:  %.2f | %v\n\n" +
                        "Alert threshold:\n" +
                        "%v %v / %.f %v\n", network.Name,
                        account.Address, network.Denom, balance, denomLarge,
                        normal_numeration, current_value, account.Balance_threshold/network.Numerator, denomLarge,
                        account.Balance_threshold, network.Denom)
    case "good":
        msg = fmt.Sprintf("✅ *%v* topped up\n" +
                        "```\n" +
                        "==== Balance ====\n" +
                        "%v: %.f\n" +
                        "%v:  %.2f | %v\n", network.Name,
                        network.Denom, balance, denomLarge,
                        normal_numeration, current_value,)
    }
    msg = fmt.Sprintf("%v\n/Used with: %v | @%v\n```\n", msg, account.Purpose, GetIp())

    // Prepare slack webhook msg
    values := map[string]string{"text": msg}
    json_data, err := json.Marshal(values)
    if err != nil {
        timeStamp(fmt.Sprintf("[NOTICE] Failed marshaling data: %v ; error: %v\n", values, err))
    }
    // Send slack webhook message
    _, err = http.Post(WALLET_SLACK_WEBHOOK, "application/json", bytes.NewBuffer(json_data))
    if err != nil {
        timeStamp(fmt.Sprintf("[ALERT] Error: failed to send alert to slack: %v error: %v\n", WALLET_SLACK_WEBHOOK, err))
    }
    fmt.Println("---- Sent low funds alert to slack ----")
}

// fetch price from coingecko
func fetchPrice(network string) ([]byte) {
    url := fmt.Sprintf("https://api.coingecko.com/api/v3/simple/price?ids=%v&vs_currencies=usd", network)
    resp, err := http.Get(url)
    if err != nil {
        timeStamp(fmt.Sprintf("[ERROR] Failed requesting: %v ; error: %v\n", url, err))
    }

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        timeStamp(fmt.Sprintf("[ERROR] Failed deserialising response body: %v ; error: %v\n", body, err))
    }

    // some networks are on coingecko indexed as e.g. juno-network
    // so let's try that if response from query bez-"-network" is nil
    if string(body) == "{}" {
        url = fmt.Sprintf("https://api.coingecko.com/api/v3/simple/price?ids=%v-network&vs_currencies=usd", network)
        resp, err = http.Get(url)
        if err != nil {
            timeStamp(fmt.Sprintf("[ERROR] Failed requesting: %v ; error: %v", url, err))
        }
        body, err = ioutil.ReadAll(resp.Body)
        if err != nil {
            timeStamp(fmt.Sprintf("[ERROR] Failed deserialising response body: %v ; error: %v", body, err))
        }
    }
    return body
}

func getValue(network string, amount float64 ) string {
    body := fetchPrice(network)
    var apiResponse map[string]interface{}
    json.Unmarshal(body, &apiResponse)

    // get the "evmos" etc index
    m , ok := apiResponse[network]

    // assert that it's a map of type [string]interface
    rate, err := m.(map[string]interface{})
    fmt.Printf("m: %+v, ok: %+v, rate: %+v, err: %+v\n", m, ok, rate, err)
    if !ok {
        timeStamp(fmt.Sprintf("[ERROR] Failed asserting map's value type as an `interface`. map: %+v", m))
        fmt.Printf("m: %+v, ok: %+v, rate: %+v, err: %+v\n", m, ok, rate, err)
        return "N/A USD"
    }

    // get value and asser that rate["usd"] has the value float64
    usdval, ok := rate["usd"].(float64)
    if !ok {
        timeStamp(fmt.Sprintf("[ERROR] Failed asserting rate['usd']'s value type as a `float64` ; rate['usd']: %v", rate["usd"]))
        return "N/A USD"
    }

    // Calculate the value of our current balance
    value := usdval * amount
    returnString := fmt.Sprintf("~%.2f USD", value)
    return returnString
}

func timeStamp(msg string) {
    log.Printf("%v\n", msg)
}

func GetIp() string {
    req, err := http.Get("http://ip-api.com/json/")
    if err != nil {
        timeStamp(fmt.Sprintf("[NOTICE] Failed to get response from ip-api.com; error: %v\n", err))
        return "ERROR"
    }
    defer req.Body.Close()
    body, err := ioutil.ReadAll(req.Body)
    if err != nil {
        timeStamp(fmt.Sprintf("[NOTICE] Failed to read from io.Reader in GetIp(). Err: %v\n", err))
        return "ERROR"
    }
    var ip IP
    json.Unmarshal(body, &ip)
    return ip.Query
}


// For Eth we send a POST request to a standard RPC
func getEthereumBalance(network Network, account Account) (*Responses, error) {
    payload := EthRPCRequest{
        JSONRPC: "2.0",
        Method:  "eth_getBalance",
        Params:  []interface{}{account.Address, "latest"},
        ID:      1,
    }
    payloadBytes, err := json.Marshal(payload)
    if err != nil {
        return nil, err
    }
    resp, err := http.Post(network.RpcUrl, "application/json", bytes.NewBuffer(payloadBytes))
    if err != nil {
        return nil, err
    }

    defer resp.Body.Close()

    // Read the response body
    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }

    // Parse the RPC response
    var ethResponse EthRPCResponse
    err = json.Unmarshal(body, &ethResponse)
    if err != nil {
        return nil, err
    }

    // make response decimal
    hexStr := strings.TrimPrefix(ethResponse.Result, "0x")
    dec := new(big.Int)
    dec.SetString(hexStr, 16)

    return &Responses{Balances: []Response{{Amount: dec.String(), Denom: "wei"}}}, nil
}



// For cosmos we send a GET request to the specified url
// and unmarshal the response into our Response struct
func getCosmosBalance(network Network, account Account) (*Responses, error) {
    url := fmt.Sprintf("%v/cosmos/bank/v1beta1/balances/%v", network.RpcUrl, account.Address)
    client := http.Client{
        Timeout: 40 * time.Second,
    }
    resp, err := client.Get(url)
    if err != nil {
        return nil, err
    }

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }

    var responses Responses
    err = json.Unmarshal(body, &responses)
    if err != nil {
        return nil, err
    }

    return &responses, nil
}


func parseBalanceResponse(responses *Responses, network Network) (float64, error, bool) {
    var resultGood bool
    var balance float64 = 0
    var err error

    for _, result := range responses.Balances {
        if result.Denom == network.Denom {
            balance, err = strconv.ParseFloat(result.Amount, 64)
            if err != nil {
                return 0, fmt.Errorf("problems converting response string: `%v` to float64 ; error: `%v`", responses.Balances[0].Amount, err), resultGood
            } else {
                resultGood = true
            }
        }
    }

    if !resultGood {
        return 0, fmt.Errorf("denom mismatch in response"), resultGood
    }

    return balance, nil, resultGood
}
