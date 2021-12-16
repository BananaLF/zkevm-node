package jsonrpc

import (
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/hermeznetwork/hermez-core/encoding"
	"github.com/hermeznetwork/hermez-core/hex"
)

type argUint64 uint64

// MarshalText marshals into text
func (b argUint64) MarshalText() ([]byte, error) {
	buf := make([]byte, 2, encoding.Base10) //nolint:gomnd
	copy(buf, `0x`)
	buf = strconv.AppendUint(buf, uint64(b), hex.Base)
	return buf, nil
}

// UnmarshalText unmarshals from text
func (b *argUint64) UnmarshalText(input []byte) error {
	str := strings.TrimPrefix(string(input), "0x")
	num, err := strconv.ParseUint(str, hex.Base, encoding.BitSize64)
	if err != nil {
		return err
	}
	*b = argUint64(num)
	return nil
}

type argBytes []byte

// MarshalText marshals into text
func (b argBytes) MarshalText() ([]byte, error) {
	return encodeToHex(b), nil
}

// UnmarshalText unmarshals from text
func (b *argBytes) UnmarshalText(input []byte) error {
	hh, err := decodeToHex(input)
	if err != nil {
		return nil
	}
	aux := make([]byte, len(hh))
	copy(aux[:], hh[:])
	*b = aux
	return nil
}

func decodeToHex(b []byte) ([]byte, error) {
	str := string(b)
	str = strings.TrimPrefix(str, "0x")
	if len(str)%2 != 0 {
		str = "0" + str
	}
	return hex.DecodeString(str)
}

func encodeToHex(b []byte) []byte {
	str := hex.EncodeToString(b)
	if len(str)%2 != 0 {
		str = "0" + str
	}
	return []byte("0x" + str)
}

// txnArgs is the transaction argument for the rpc endpoints
type txnArgs struct {
	From     *common.Address
	To       *common.Address
	Gas      *argUint64
	GasPrice *argBytes
	Value    *argBytes
	Input    *argBytes
	Data     *argBytes
	Nonce    *argUint64
}

// ToTransaction transforms txnArgs into a Transaction
func (arg *txnArgs) ToTransaction() *types.Transaction {
	nonce := uint64(0)
	if arg.Nonce != nil {
		nonce = uint64(*arg.Nonce)
	}

	gas := uint64(0)
	if arg.Gas != nil {
		gas = uint64(*arg.Gas)
	}

	gasPrice := hex.DecodeHexToBig(string(*arg.GasPrice))

	value := big.NewInt(0)
	if arg.Value != nil {
		value = hex.DecodeHexToBig(string(*arg.Value))
	}

	data := []byte{}
	if arg.Data != nil {
		data = *arg.Data
	}

	tx := types.NewTransaction(nonce, *arg.To, value, gas, gasPrice, data)

	return tx
}