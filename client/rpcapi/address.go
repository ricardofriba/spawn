package rpcapi

import (
	"encoding/hex"

	"github.com/ParallelCoinTeam/duod/lib/btc"
	//"github.com/ParallelCoinTeam/duod/client/common"
	// "github.com/ParallelCoinTeam/duod/lib/L"
)

/*

{"result":
	{"isvalid":true,
	"address":"mqzwxBkSH1UKqEAjGwvkj6aV5Gc6BtBCSs",
	"scriptPubKey":"76a91472fc9e6b1bbbd40a66653989a758098bfbf1b54788ac",
	"ismine":false,
	"iswatchonly":false,
	"isscript":false
}
*/

// ValidAddressResponse -
type ValidAddressResponse struct {
	IsValid      bool   `json:"isvalid"`
	Address      string `json:"address"`
	ScriptPubKey string `json:"scriptPubKey"`
	IsMine       bool   `json:"ismine"`
	IsWatchOnly  bool   `json:"iswatchonly"`
	IsScript     bool   `json:"isscript"`
}

// InvalidAddressResponse -
type InvalidAddressResponse struct {
	IsValid bool `json:"isvalid"`
}

// ValidateAddress -
func ValidateAddress(addr string) interface{} {
	a, e := btc.NewAddrFromString(addr)
	if e != nil {
		return new(InvalidAddressResponse)
	}
	res := new(ValidAddressResponse)
	res.IsValid = true
	res.Address = addr
	res.ScriptPubKey = hex.EncodeToString(a.OutScript())
	return res
	//res.IsMine = false
	//res.IsWatchOnly = false
	//res.IsScript = false
}
