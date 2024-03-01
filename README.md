# wallet-bot
This script continuously monitors a range of blockchain wallets, across (currently) ETH and cosmos.

This can be used to track balances of an operator's relayers, restake wallets, and EVM accounts.

Additionally, it will alert if there are problems with the specified RPC endpoint.

`networks.json` includes the following information about each network:

- **name**: the name of the network, as it is indexed on [cosmos.directory](https://cosmos.directory)
- **rpcUrl**: optionally specify which endpoint to use
- **address**: the wallet address
- **balance_threshold**: the amount below which an account will be considered as low on funds, and an alert sent on Slack
- **bad_request_threshold**: the number of hours of unsuccessful requests, after which we will be sent an alert of RPC issues over Slack  
- **denom** denom: the denomination of the relevant coin. This would be e.g. uatom, aevmos, or wei
- **coingecko_name**: the name to use when getting the price to calculate the value in alerts through the coingecko API

## Running it
To run it, first specify your slack webhooks in `WALLET_SLACK_WEBHOOK` `RPC_SLACK_WEBHOOK`. These will be used to send wallet alerts, and rpc failure alerts, respectively.

Then, simply run it:
```
go build check_balances.go
./check_balances
```

