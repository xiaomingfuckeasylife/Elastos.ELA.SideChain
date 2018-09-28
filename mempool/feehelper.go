package mempool

import (
	"bytes"
	"errors"

	"github.com/elastos/Elastos.ELA.SideChain/blockchain"
	"github.com/elastos/Elastos.ELA.SideChain/config"
	"github.com/elastos/Elastos.ELA.SideChain/core"

	. "github.com/elastos/Elastos.ELA.Utility/common"
)

type FeeHelper struct {
	db blockchain.IChainStore
}

func NewFeeHelper(db blockchain.IChainStore) *FeeHelper {
	return &FeeHelper{db: db}
}

func (h *FeeHelper) GetTxFee(tx *core.Transaction, assetId Uint256) Fixed64 {
	feeMap, err := h.GetTxFeeMap(tx)
	if err != nil {
		return 0
	}

	return feeMap[assetId]
}

func (h *FeeHelper) GetTxFeeMap(tx *core.Transaction) (map[Uint256]Fixed64, error) {
	feeMap := make(map[Uint256]Fixed64)

	if tx.IsRechargeToSideChainTx() {
		depositPayload := tx.Payload.(*core.PayloadRechargeToSideChain)
		mainChainTransaction := new(core.Transaction)
		reader := bytes.NewReader(depositPayload.MainChainTransaction)
		if err := mainChainTransaction.Deserialize(reader); err != nil {
			return nil, errors.New("GetTxFeeMap mainChainTransaction deserialize failed")
		}

		crossChainPayload := mainChainTransaction.Payload.(*core.PayloadTransferCrossChainAsset)

		for _, v := range tx.Outputs {
			for i := 0; i < len(crossChainPayload.CrossChainAddresses); i++ {
				targetAddress, err := v.ProgramHash.ToAddress()
				if err != nil {
					return nil, err
				}
				if targetAddress == crossChainPayload.CrossChainAddresses[i] {
					mcAmount := mainChainTransaction.Outputs[crossChainPayload.OutputIndexes[i]].Value

					amount, ok := feeMap[v.AssetID]
					if ok {
						feeMap[v.AssetID] = amount + Fixed64(float64(mcAmount)*config.Parameters.ExchangeRate) - v.Value
					} else {
						feeMap[v.AssetID] = Fixed64(float64(mcAmount)*config.Parameters.ExchangeRate) - v.Value
					}
				}
			}
		}

		return feeMap, nil
	}

	reference, err := h.db.GetTxReference(tx)
	if err != nil {
		return nil, err
	}

	var inputs = make(map[Uint256]Fixed64)
	var outputs = make(map[Uint256]Fixed64)
	for _, v := range reference {
		amout, ok := inputs[v.AssetID]
		if ok {
			inputs[v.AssetID] = amout + v.Value
		} else {
			inputs[v.AssetID] = v.Value
		}
	}

	for _, v := range tx.Outputs {
		amout, ok := outputs[v.AssetID]
		if ok {
			outputs[v.AssetID] = amout + v.Value
		} else {
			outputs[v.AssetID] = v.Value
		}
	}

	//calc the balance of input vs output
	for outputAssetid, outputValue := range outputs {
		if inputValue, ok := inputs[outputAssetid]; ok {
			feeMap[outputAssetid] = inputValue - outputValue
		} else {
			feeMap[outputAssetid] -= outputValue
		}
	}
	for inputAssetid, inputValue := range inputs {
		if _, exist := feeMap[inputAssetid]; !exist {
			feeMap[inputAssetid] += inputValue
		}
	}
	return feeMap, nil
}