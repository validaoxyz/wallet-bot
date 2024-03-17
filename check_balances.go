package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io/ioutil"
    "log"
    "math/big"
    "net/http"
    "os"
    "strconv"
    "strings"
    "time"
)

type BlockchainNetworks struct {
    Networks[] Blockchain `json:"blockchainNetworks"`
}

type Blockchain struct {
    Identifier string `json:"identifier"`
    Kind string `json:"kind"`
    Endpoint string `json:"endpoint"`
    Wallets[] Wallet `json:"wallets"`
    ConversionFactor float64 `json:"conversionFactor"`
    FailureThreshold float64 `json:"failureThreshold"`
    CurrencyUnit string `json:"currencyUnit"`
    PriceSource string `json:"priceSource"`
}

type Wallet struct {
    WalletAddress string `json:"walletAddress"`
    UseCase string `json:"useCase"`
    MinBalance float64 `json:"minBalance"`
    EndpointFailures int `json:"-"`
    IsBelowThreshold bool `json:"-"`
}

type AccountBalances struct {
    BalanceDetails[] BalanceDetail `json:"balanceDetails"`
}

type BalanceDetail struct {
    Currency string `json:"currency"`
    Amount string `json:"amount"`
}

type RemoteIP struct {
    IPAddress string `json:"-"`
}

type EthRPCPayload struct {
    Version string `json:"jsonrpc"`
    Action string `json:"method"`
    Params[] interface {}
    `json:"params"`
    RequestID int `json:"id"`
}

type EthRPCResult struct {
    ResponseID int `json:"id"`
    Data string `json:"result"`
    RPCError * RPCErrorDetail `json:"error,omitempty"`
}

type RPCErrorDetail struct {
    ErrorCode int `json:"code"`
    ErrorMessage string `json:"message"`
}

const (
    WebhookBalance = ""
    WebhookRPC = ""
    HoursPerDay = 24
)

var (
    RPCErrorThreshold int = 0 BalanceCheckInterval float64 = 0.5
)

func main() {
    blockchains: = loadBlockchainConfig()

    for {
        updateConfigurations( & blockchains)

        var encounteredIssues bool
        for idx, chain: = range blockchains.Networks {
            RPCErrorThreshold = int(chain.FailureThreshold / BalanceCheckInterval)
            for i, wallet: = range chain.Wallets {
                balances, err: = getBalance(chain, wallet)
                if err != nil {
                    blockchains.Networks[idx].Wallets[i].EndpointFailures++
                        encounteredIssues = true
                    checkRPCHealth(chain, wallet)
                    continue
                }

                balance, err, isValid: = analyzeBalance(balances, chain)
                if isValid {
                    handleBalanceThreshold(chain, wallet, balance)
                } else {
                    blockchains.Networks[idx].Wallets[i].EndpointFailures++
                        encounteredIssues = true
                    checkRPCHealth(chain, wallet)
                }

                // Reset RPC down count after successful balance fetch
                if blockchains.Networks[idx].Wallets[i].EndpointFailures > 0 {
                    blockchains.Networks[idx].Wallets[i].EndpointFailures = 0
                }
            }
        }

        logCompletion(encounteredIssues)
        time.Sleep(time.Duration(60 * BalanceCheckInterval) * time.Minute)
    }
}
func loadBlockchainConfig() BlockchainNetworks {
    configFile, err: = os.Open("blockchainConfig.json")
    if err != nil {
        logEvent(fmt.Sprintf("[ERROR] Could not open blockchainConfig.json; error: `%v`", err))
    }
    defer configFile.Close()

    byteValue, err: = ioutil.ReadAll(configFile)
    if err != nil {
        logEvent(fmt.Sprintf("[ERROR] Failed to read content from blockchainConfig.json; error: `%v`", err))
    }
    var blockchains BlockchainNetworks
    json.Unmarshal(byteValue, & blockchains)

    for idx, chain: = range blockchains.Networks {
        blockchains.Networks[idx].Endpoint = determineEndpoint(chain)
    }
    return blockchains
}
func updateConfigurations(currentBlockchains * BlockchainNetworks) {
    updatedBlockchains: = loadBlockchainConfig()
    for idx,
    chain: = range updatedBlockchains.Networks {
        for i, _: = range chain.Wallets {
            updatedBlockchains.Networks[idx].Wallets[i].IsBelowThreshold = currentBlockchains.Networks[idx].Wallets[i].IsBelowThreshold
            updatedBlockchains.Networks[idx].Wallets[i].EndpointFailures = currentBlockchains.Networks[idx].Wallets[i].EndpointFailures
        }
    } * currentBlockchains = updatedBlockchains
}

func fetchEthBalance(blockchain Blockchain, wallet Wallet)( * AccountBalances, error) {
    rpcPayload: = EthRPCPayload {
        Version: "2.0",
        Action: "eth_getBalance",
        Params: [] interface {} {
            wallet.WalletAddress, "latest"
        },
        RequestID: 1,
    }
    payloadBytes,
    err: = json.Marshal(rpcPayload)
    if err != nil {
        return nil, err
    }
    response,
    err: = http.Post(blockchain.Endpoint, "application/json", bytes.NewBuffer(payloadBytes))
    if err != nil {
        return nil, err
    }
    defer response.Body.Close()

    body,
    err: = ioutil.ReadAll(response.Body)
    if err != nil {
        return nil, err
    }

    var ethResult EthRPCResult
    if err: = json.Unmarshal(body, & ethResult);err != nil {
        return nil, err
    }

    hexStr: = strings.TrimPrefix(ethResult.Data, "0x")
    dec: = new(big.Int)
    dec.SetString(hexStr, 16)

    return &AccountBalances {
        BalanceDetails: [] BalanceDetail {
            {
                Currency: blockchain.CurrencyUnit,
                Amount: dec.String()
            }
        }
    },
    nil
}

func fetchCosmosBalance(blockchain Blockchain, wallet Wallet)( * AccountBalances, error) {
    url: = fmt.Sprintf("%v/cosmos/bank/v1beta1/balances/%v", determineEndpoint(blockchain), wallet.WalletAddress)
    client: = http.Client {
        Timeout: 40 * time.Second
    }
    resp,
    err: = client.Get(url)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    body,
    err: = ioutil.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }

    // Correctly defining the structure to match the real API response.
    var apiResponse struct {
        Balances[] struct {
            Denom string `json:"denom"`
            Amount string `json:"amount"`
        }
        `json:"balances"`
        Pagination struct {
            NextKey string `json:"next_key"`
            Total string `json:"total"`
        }
        `json:"pagination"`
    }

    if err: = json.Unmarshal(body, & apiResponse);err != nil {
        logEvent(fmt.Sprintf("Raw API Response: %s", string(body))) // For debugging
        return nil, err
    }

    // Convert the API response to the AccountBalances structure expected by the rest of the program.
    var balances AccountBalances
    for _,
    balance: = range apiResponse.Balances {
        balances.BalanceDetails = append(balances.BalanceDetails, BalanceDetail {
            Currency: balance.Denom,
            Amount: balance.Amount,
        })
    }

        return &balances, nil
}


func analyzeBalance(balances * AccountBalances, blockchain Blockchain)(float64, error, bool) {
    var resultGood bool
    var balance float64 = 0
    var err error

    for _, detail: = range balances.BalanceDetails {
        if detail.Currency == blockchain.CurrencyUnit {
            balance, err = strconv.ParseFloat(detail.Amount, 64)
            if err != nil {
                return 0, fmt.Errorf("error converting string `%v` to float64; error: `%v`", detail.Amount, err), resultGood
            }
            resultGood = true
            break
        }
    }

    if !resultGood {
        return 0, fmt.Errorf("currency unit mismatch in response"), resultGood
    }

    return balance, nil, resultGood
}


func determineEndpoint(chain Blockchain) string {
    if chain.Endpoint == "" {
        return fmt.Sprintf("https://rest.cosmos.directory/%v", chain.Identifier)
    }
    return chain.Endpoint
}



func sendWebhookNotification(webhookURL, message string) {
    payload: = map[string] string {
        "text": message
    }
    payloadBytes,
    err: = json.Marshal(payload)
    if err != nil {
        logEvent(fmt.Sprintf("Error marshaling Webhook payload: %v", err))
        return
    }

    resp,
    err: = http.Post(webhookURL, "application/json", bytes.NewBuffer(payloadBytes))
    if err != nil {
        logEvent(fmt.Sprintf("Failed to send Webhook notification: %v", err))
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusOK {
        logEvent("Webhook notification sent successfully")
    } else {
        logEvent(fmt.Sprintf("Failed to send Webhook notification, status code: %d", resp.StatusCode))
    }
}

func handleLowBalanceAlert(chain Blockchain, wallet Wallet, balance float64) {
    message: = fmt.Sprintf("💔 [ALERT] Wallet %s in %s is low on funds. Balance: %.f %v", wallet.UseCase, chain.Identifier, balance, chain.CurrencyUnit)
    logEvent(message)
    if !wallet.IsBelowThreshold {
        sendWebhookNotification(WebhookBalance, message)
        wallet.IsBelowThreshold = true
    }
}

func notifyBalanceChange(status string, balance float64, blockchain Blockchain, wallet Wallet) {
    var message string
    switch status {
        case "low":
            message = fmt.Sprintf("[ALERT] Wallet %s in %s is low on funds. Current balance: %.f %v", wallet.UseCase, blockchain.Identifier, balance, blockchain.CurrencyUnit)
        case "restored":
            message = fmt.Sprintf("[INFO] Wallet %s in %s has been replenished. New balance: %.f %v", wallet.UseCase, blockchain.Identifier, balance, blockchain.CurrencyUnit)
    }
    logEvent(message)
    sendWebhookNotification(WebhookBalance, message)
}

func notifyRPCIssue(status string, blockchain Blockchain, wallet Wallet) {
    var message string
    if status == "issue" {
        message = fmt.Sprintf("[ALERT] RPC endpoint for %s has issues. Endpoint: %s, Wallet: %s", blockchain.Identifier, blockchain.Endpoint, wallet.WalletAddress)
    }
    logEvent(message)
    sendWebhookNotification(WebhookRPC, message)
}


func handleBalanceThreshold(chain Blockchain, wallet Wallet, balance float64) {
    balanceThresholdCrossed: = balance < wallet.MinBalance
    if balanceThresholdCrossed && !wallet.IsBelowThreshold {
        message: = fmt.Sprintf("💔 [ALERT] Wallet %s in %s is low on funds. Balance: %.2f %v", wallet.UseCase, chain.Identifier, balance, chain.CurrencyUnit)
        logEvent(message)
        sendWebhookNotification(WebhookBalance, message)
        wallet.IsBelowThreshold = true // Mark as notified
    } else if !balanceThresholdCrossed && wallet.IsBelowThreshold {
        message: = fmt.Sprintf("💚 [INFO] Wallet %s in %s has been replenished. New balance: %.2f %v", wallet.UseCase, chain.Identifier, balance, chain.CurrencyUnit)
        logEvent(message)
        sendWebhookNotification(WebhookBalance, message)
        wallet.IsBelowThreshold = false // Reset notification flag
    } else if balanceThresholdCrossed {
        // Log but don't notify for subsequent low balance checks until balance is restored
        logEvent(fmt.Sprintf("💔 Wallet %s in %s remains low on funds. Balance: %.2f %v", wallet.UseCase, chain.Identifier, balance, chain.CurrencyUnit))
    }
}

func getBalance(chain Blockchain, wallet Wallet)( * AccountBalances, error) {
    var balances * AccountBalances
    var err error
    switch chain.Kind {
        case "ethereum":
            balances, err = fetchEthBalance(chain, wallet)
        case "cosmos":
            balances, err = fetchCosmosBalance(chain, wallet)
        default:
            err = fmt.Errorf("unsupported blockchain kind: %s", chain.Kind)
    }
    return balances, err
}

func checkRPCHealth(chain Blockchain, wallet Wallet) {
    if wallet.EndpointFailures >= RPCErrorThreshold {
        message: = fmt.Sprintf("[ALERT] RPC endpoint for %s has issues. Endpoint: %s, Wallet: %s, Failures: %d", chain.Identifier, chain.Endpoint, wallet.WalletAddress, wallet.EndpointFailures)
        logEvent(message)
        sendWebhookNotification(WebhookRPC, message)
    }
}


func logCompletion(issues bool) {
    if !issues {
        logEvent("Run completed successfully with all networks checked.")
    } else {
        logEvent("Run completed with issues in one or more networks.")
    }
}

func logEvent(msg string) {
    log.Printf("%v\n", msg)
}
