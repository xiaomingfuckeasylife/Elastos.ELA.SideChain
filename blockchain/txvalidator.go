package blockchain

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/elastos/Elastos.ELA.SideChain/common"
	"github.com/elastos/Elastos.ELA.SideChain/config"
	"github.com/elastos/Elastos.ELA.SideChain/core"
	. "github.com/elastos/Elastos.ELA.SideChain/errors"
	"github.com/elastos/Elastos.ELA.SideChain/log"

	. "github.com/elastos/Elastos.ELA.Utility/common"
	"github.com/elastos/Elastos.ELA.Utility/crypto"
	. "github.com/elastos/Elastos.ELA/bloom"
	ela "github.com/elastos/Elastos.ELA/core"
)

// CheckTransactionSanity verifys received single transaction
func CheckTransactionSanity(txn *core.Transaction) ErrCode {

	if err := CheckTransactionSize(txn); err != nil {
		log.Warn("[CheckTransactionSize],", err)
		return ErrTransactionSize
	}

	if err := CheckTransactionInput(txn); err != nil {
		log.Warn("[CheckTransactionInput],", err)
		return ErrInvalidInput
	}

	if err := CheckTransactionOutput(txn); err != nil {
		log.Warn("[CheckTransactionOutput],", err)
		return ErrInvalidOutput
	}

	if err := CheckAssetPrecision(txn); err != nil {
		log.Warn("[CheckAssetPrecesion],", err)
		return ErrAssetPrecision
	}

	if err := CheckAttributeProgram(txn); err != nil {
		log.Warn("[CheckAttributeProgram],", err)
		return ErrAttributeProgram
	}

	if err := CheckTransactionPayload(txn); err != nil {
		log.Warn("[CheckTransactionPayload],", err)
		return ErrTransactionPayload
	}

	// check iterms above for Coinbase transaction
	if txn.IsCoinBaseTx() {
		return Success
	}

	return Success
}

// CheckTransactionContext verifys a transaction with history transaction in ledger
func CheckTransactionContext(txn *core.Transaction) ErrCode {
	// check if duplicated with transaction in ledger
	if exist := DefaultLedger.Store.IsTxHashDuplicate(txn.Hash()); exist {
		log.Info("[CheckTransactionContext] duplicate transaction check faild.")
		return ErrTxHashDuplicate
	}

	if txn.IsCoinBaseTx() {
		return Success
	}

	if err := CheckTransactionSignature(txn); err != nil {
		log.Warn("[CheckTransactionSignature],", err)
		return ErrTransactionSignature
	}

	if txn.IsRechargeToSideChainTx() {
		if err := CheckRechargeToSideChainTransaction(txn); err != nil {
			log.Warn("[CheckRechargeToSideChainTransaction],", err)
			return ErrRechargeToSideChain
		}
		return Success
	}

	if txn.IsTransferCrossChainAssetTx() {
		if err := CheckTransferCrossChainAssetTransaction(txn); err != nil {
			log.Warn("[CheckTransferCrossChainAssetTransaction],", err)
			return ErrInvalidOutput
		}
	}

	if txn.IsRegisterAssetTx() {
		if err := CheckRegisterAssetTransaction(txn); err != nil {
			log.Warn("[CheckRegisterAssetTransaction],", err)
			return ErrInvalidOutput
		}
	}

	// check double spent transaction
	if DefaultLedger.IsDoubleSpend(txn) {
		log.Info("[CheckTransactionContext] IsDoubleSpend check faild.")
		return ErrDoubleSpend
	}

	if err := CheckTransactionUTXOLock(txn); err != nil {
		log.Warn("[CheckTransactionUTXOLock],", err)
		return ErrUTXOLocked
	}

	if err := CheckTransactionFee(txn); err != nil {
		log.Warn("[CheckTransactionFee],", err)
		return ErrTransactionBalance
	}

	// check referenced Output value
	for _, input := range txn.Inputs {
		referHash := input.Previous.TxID
		referTxnOutIndex := input.Previous.Index
		referTxn, _, err := DefaultLedger.Store.GetTransaction(referHash)
		if err != nil {
			log.Warn("Referenced transaction can not be found", BytesToHexString(referHash.Bytes()))
			return ErrUnknownReferedTxn
		}
		referTxnOut := referTxn.Outputs[referTxnOutIndex]
		if referTxnOut.AssetID.IsEqual(DefaultLedger.Blockchain.AssetID) {
			if referTxnOut.Value <= 0 {
				log.Warn("Value of referenced transaction output is invalid")
				return ErrInvalidReferedTxn
			}
		} else {
			if referTxnOut.TokenValue.Sign() <= 0 {
				log.Warn("TokenValue of referenced transaction output is invalid")
			}
		}

		// coinbase transaction only can be spent after got SpendCoinbaseSpan times confirmations
		if referTxn.IsCoinBaseTx() {
			lockHeight := referTxn.LockTime
			currentHeight := DefaultLedger.Store.GetHeight()
			if currentHeight-lockHeight < config.Parameters.ChainParam.SpendCoinbaseSpan {
				return ErrIneffectiveCoinbase
			}
		}
	}

	return Success
}

//validate the transaction of duplicate UTXO input
func CheckTransactionInput(txn *core.Transaction) error {
	if txn.IsCoinBaseTx() {
		if len(txn.Inputs) != 1 {
			return errors.New("coinbase must has only one input")
		}
		coinbaseInputHash := txn.Inputs[0].Previous.TxID
		coinbaseInputIndex := txn.Inputs[0].Previous.Index
		//TODO :check sequence
		if !coinbaseInputHash.IsEqual(EmptyHash) || coinbaseInputIndex != math.MaxUint16 {
			return errors.New("invalid coinbase input")
		}

		return nil
	}

	if txn.IsRechargeToSideChainTx() {
		return nil
	}

	if len(txn.Inputs) <= 0 {
		return errors.New("transaction has no inputs")
	}
	for i, utxoin := range txn.Inputs {
		if utxoin.Previous.TxID.IsEqual(EmptyHash) && (utxoin.Previous.Index == math.MaxUint16) {
			return errors.New("invalid transaction input")
		}
		for j := 0; j < i; j++ {
			if utxoin.Previous.IsEqual(txn.Inputs[j].Previous) {
				return errors.New("duplicated transaction inputs")
			}
		}
	}

	return nil
}

func CheckTransactionOutput(txn *core.Transaction) error {
	if txn.IsCoinBaseTx() {
		if len(txn.Outputs) < 2 {
			return errors.New("coinbase output is not enough, at least 2")
		}

		var totalReward = Fixed64(0)
		var foundationReward = Fixed64(0)
		for _, output := range txn.Outputs {
			totalReward += output.Value
			if output.ProgramHash.IsEqual(FoundationAddress) {
				foundationReward += output.Value
			}
		}
		if Fixed64(foundationReward) < Fixed64(float64(totalReward)*0.3) {
			return errors.New("Reward to foundation in coinbase < 30%")
		}

		return nil
	}

	if len(txn.Outputs) < 1 {
		return errors.New("transaction has no outputs")
	}

	// check if output address is valid
	for _, output := range txn.Outputs {
		if output.AssetID == EmptyHash {
			return errors.New("asset id is nil")
		} else if output.AssetID == DefaultLedger.Blockchain.AssetID {
			if output.Value < 0 || output.TokenValue.Sign() != 0 {
				return errors.New("invalid transaction output with ela asset id")
			}
		} else {
			if txn.IsRechargeToSideChainTx() || txn.IsTransferCrossChainAssetTx() {
				return errors.New("cross chain asset tx asset id should only be ela asset id")
			}
			if output.TokenValue.Sign() < 0 || output.Value != 0 {
				return errors.New("invalid transaction output with token asset id")
			}
		}
		if !CheckOutputProgramHash(output.ProgramHash) {
			return errors.New("output address is invalid")
		}
	}
	return nil
}

func CheckOutputProgramHash(programHash Uint168) bool {
	var empty = Uint168{}
	prefix := programHash[0]
	if prefix == PrefixStandard ||
		prefix == PrefixMultisig ||
		prefix == PrefixCrossChain ||
		prefix == PrefixRegisterId ||
		programHash == empty {
		return true
	}
	return false
}

func CheckTransactionUTXOLock(txn *core.Transaction) error {
	if txn.IsCoinBaseTx() {
		return nil
	}
	if len(txn.Inputs) <= 0 {
		return errors.New("Transaction has no inputs")
	}
	references, err := DefaultLedger.Store.GetTxReference(txn)
	if err != nil {
		return fmt.Errorf("GetReference failed: %s", err)
	}
	for input, output := range references {

		if output.OutputLock == 0 {
			//check next utxo
			continue
		}
		if input.Sequence != math.MaxUint32-1 {
			return errors.New("Invalid input sequence")
		}
		if txn.LockTime < output.OutputLock {
			return errors.New("UTXO output locked")
		}
	}
	return nil
}

func CheckTransactionSize(txn *core.Transaction) error {
	size := txn.GetSize()
	if size <= 0 || size > config.Parameters.MaxBlockSize {
		return fmt.Errorf("Invalid transaction size: %d bytes", size)
	}

	return nil
}

func CheckAssetPrecision(txn *core.Transaction) error {
	if txn.TxType == core.RegisterAsset {
		return nil
	}

	if len(txn.Outputs) == 0 {
		return nil
	}
	assetOutputs := make(map[Uint256][]*core.Output, len(txn.Outputs))

	for _, v := range txn.Outputs {
		assetOutputs[v.AssetID] = append(assetOutputs[v.AssetID], v)
	}
	for k, outputs := range assetOutputs {
		asset, err := DefaultLedger.GetAsset(k)
		if err != nil {
			return errors.New("The asset not exist in local blockchain.")
		}
		precision := asset.Precision
		for _, output := range outputs {
			if output.AssetID.IsEqual(DefaultLedger.Blockchain.AssetID) {
				if !checkAmountPrecise(output.Value, precision, 8) {
					return errors.New("Invalide ela asset value,out of precise.")
				}
			} else {
				if !checkAmountPrecise(output.Value, precision, 18) {
					return errors.New("Invalide asset value,out of precise.")
				}
			}
		}
	}
	return nil
}

func CheckTransactionFee(txn *core.Transaction) error {
	var elaInputAmount = Fixed64(0)
	var tokenInputAmount = new(big.Int).SetInt64(0)
	var elaOutputAmount = Fixed64(0)
	var tokenOutputAmount = new(big.Int).SetInt64(0)
	for _, output := range txn.Outputs {
		if output.AssetID.IsEqual(DefaultLedger.Blockchain.AssetID) {
			elaOutputAmount += output.Value
		} else {
			tokenOutputAmount.Add(tokenOutputAmount, &(output.TokenValue))
		}
	}

	references, err := DefaultLedger.Store.GetTxReference(txn)
	if err != nil {
		return err
	}

	for _, output := range references {
		if output.AssetID.IsEqual(DefaultLedger.Blockchain.AssetID) {
			elaInputAmount += output.Value
		} else {
			tokenInputAmount.Add(tokenInputAmount, &(output.TokenValue))
		}
	}
	for _, output := range txn.Outputs {
		if output.AssetID.IsEqual(DefaultLedger.Blockchain.AssetID) {
			elaOutputAmount += output.Value
		} else {
			tokenOutputAmount.Add(tokenOutputAmount, &(output.TokenValue))
		}
	}

	elaBalance := elaInputAmount - elaOutputAmount
	if txn.IsTransferCrossChainAssetTx() || txn.IsRechargeToSideChainTx() {
		if int(elaBalance) < config.Parameters.MinCrossChainTxFee {
			return errors.New("crosschain transaction fee is not enough")
		}
	} else {
		if int(elaBalance) < config.Parameters.PowConfiguration.MinTxFee {
			return errors.New("transaction fee is not enough")
		}
	}

	tokenBalance := tokenInputAmount.Sub(tokenInputAmount, tokenOutputAmount)
	if tokenBalance.Sign() != 0 {
		return errors.New("token amount is not balanced")
	}
	return nil
}

func CheckAttributeProgram(txn *core.Transaction) error {
	// Check attributes
	for _, attr := range txn.Attributes {
		if !core.IsValidAttributeType(attr.Usage) {
			return fmt.Errorf("invalid attribute usage %v", attr.Usage)
		}
	}

	// Check programs
	for _, program := range txn.Programs {
		if program.Code == nil {
			return fmt.Errorf("invalid program code nil")
		}
		if program.Parameter == nil {
			return fmt.Errorf("invalid program parameter nil")
		}
		_, err := crypto.ToProgramHash(program.Code)
		if err != nil {
			return fmt.Errorf("invalid program code %x", program.Code)
		}
	}
	return nil
}

func CheckTransactionSignature(txn *core.Transaction) error {
	return VerifySignature(txn)
}

func checkAmountPrecise(amount Fixed64, precision byte, assetPrecision byte) bool {
	return amount.IntValue()%int64(math.Pow10(int(assetPrecision-precision))) == 0
}

func CheckTransactionPayload(txn *core.Transaction) error {
	switch pld := txn.Payload.(type) {
	case *core.PayloadRegisterAsset:
		if pld.Asset.Precision < core.MinPrecision || pld.Asset.Precision > core.MaxPrecision {
			return errors.New("Invalide asset Precision.")
		}
		txHash := txn.Hash()
		if txHash.IsEqual(DefaultLedger.Blockchain.AssetID) {
			if !checkAmountPrecise(pld.Amount, pld.Asset.Precision, 8) {
				return errors.New("Invalide ela asset value,out of precise.")
			}
		} else {
			if !checkAmountPrecise(pld.Amount, pld.Asset.Precision, 18) {
				return errors.New("Invalide asset value,out of precise.")
			}
		}

	case *core.PayloadTransferAsset:
	case *core.PayloadRecord:
	case *core.PayloadCoinBase:
	case *core.PayloadRechargeToSideChain:
	case *core.PayloadTransferCrossChainAsset:
	case *core.PayloadRegisterIdentification:
	default:
		return errors.New("[txValidator],invalidate transaction payload type.")
	}
	return nil
}

func CheckRechargeToSideChainTransaction(txn *core.Transaction) error {
	proof := new(MerkleProof)
	mainChainTransaction := new(ela.Transaction)

	payloadRecharge, ok := txn.Payload.(*core.PayloadRechargeToSideChain)
	if !ok {
		return errors.New("Invalid recharge to side chain payload type")
	}

	if config.Parameters.ExchangeRate <= 0 {
		return errors.New("Invalid config exchange rate")
	}

	reader := bytes.NewReader(payloadRecharge.MerkleProof)
	if err := proof.Deserialize(reader); err != nil {
		return errors.New("RechargeToSideChain payload deserialize failed")
	}
	reader = bytes.NewReader(payloadRecharge.MainChainTransaction)
	if err := mainChainTransaction.Deserialize(reader); err != nil {
		return errors.New("RechargeToSideChain mainChainTransaction deserialize failed")
	}

	mainchainTxhash := mainChainTransaction.Hash()
	if exist := DefaultLedger.Store.IsMainchainTxHashDuplicate(mainchainTxhash); exist {
		return errors.New("Duplicate mainchain transaction hash in paylod")
	}

	payloadObj, ok := mainChainTransaction.Payload.(*ela.PayloadTransferCrossChainAsset)
	if !ok {
		return errors.New("Invalid payload ela.PayloadTransferCrossChainAsset")
	}

	genesisHash, _ := DefaultLedger.Store.GetBlockHash(uint32(0))
	genesisProgramHash, err := common.GetGenesisProgramHash(genesisHash)
	if err != nil {
		return errors.New("Genesis block bytes to program hash failed")
	}

	//check output fee and rate
	var oriOutputTotalAmount Fixed64
	for i := 0; i < len(payloadObj.CrossChainAddresses); i++ {
		if mainChainTransaction.Outputs[payloadObj.OutputIndexes[i]].ProgramHash.IsEqual(*genesisProgramHash) {
			if payloadObj.CrossChainAmounts[i] < 0 || payloadObj.CrossChainAmounts[i] >
				mainChainTransaction.Outputs[payloadObj.OutputIndexes[i]].Value-Fixed64(config.Parameters.MinCrossChainTxFee) {
				return errors.New("Invalid transaction cross chain amount")
			}

			crossChainAmount := Fixed64(float64(payloadObj.CrossChainAmounts[i]) * config.Parameters.ExchangeRate)
			oriOutputTotalAmount += crossChainAmount

			programHash, err := Uint168FromAddress(payloadObj.CrossChainAddresses[i])
			if err != nil {
				return errors.New("Invalid transaction payload cross chain address")
			}
			isContained := false
			for _, output := range txn.Outputs {
				if output.ProgramHash == *programHash && output.Value == crossChainAmount {
					isContained = true
					break
				}
			}
			if !isContained {
				return errors.New("Invalid transaction outputs")
			}
		}
	}

	var targetOutputTotalAmount Fixed64
	for _, output := range txn.Outputs {
		if output.Value < 0 {
			return errors.New("Invalid transaction output value")
		}
		targetOutputTotalAmount += output.Value
	}

	if targetOutputTotalAmount != oriOutputTotalAmount {
		return errors.New("Output and fee verify failed")
	}

	return nil
}

func CheckTransferCrossChainAssetTransaction(txn *core.Transaction) error {
	payloadObj, ok := txn.Payload.(*core.PayloadTransferCrossChainAsset)
	if !ok {
		return errors.New("Invalid transfer cross chain asset payload type")
	}
	if len(payloadObj.CrossChainAddresses) == 0 ||
		len(payloadObj.CrossChainAddresses) > len(txn.Outputs) ||
		len(payloadObj.CrossChainAddresses) != len(payloadObj.CrossChainAmounts) ||
		len(payloadObj.CrossChainAmounts) != len(payloadObj.OutputIndexes) {
		return errors.New("Invalid transaction payload content")
	}

	//check cross chain output index in payload
	outputIndexMap := make(map[uint64]struct{})
	for _, outputIndex := range payloadObj.OutputIndexes {
		if _, exist := outputIndexMap[outputIndex]; exist || int(outputIndex) >= len(txn.Outputs) {
			return errors.New("Invalid transaction payload cross chain index")
		}
		outputIndexMap[outputIndex] = struct{}{}
	}

	//check address in outputs and payload
	var crossChainCount int
	for _, output := range txn.Outputs {
		if output.ProgramHash.IsEqual(Uint168{}) {
			crossChainCount++
		}
	}
	if len(payloadObj.CrossChainAddresses) != crossChainCount {
		return errors.New("Invalid transaction cross chain counts")
	}
	for _, address := range payloadObj.CrossChainAddresses {
		if address == "" {
			return errors.New("Invalid transaction cross chain address")
		}
		programHash, err := Uint168FromAddress(address)
		if err != nil {
			return errors.New("Invalid transaction cross chain address")
		}
		if !bytes.Equal(programHash[0:1], []byte{PrefixStandard}) && !bytes.Equal(programHash[0:1], []byte{PrefixMultisig}) {
			return errors.New("Invalid transaction cross chain address")
		}
	}

	//check cross chain amount in payload
	for i := 0; i < len(payloadObj.OutputIndexes); i++ {
		if !txn.Outputs[payloadObj.OutputIndexes[i]].ProgramHash.IsEqual(Uint168{}) {
			return errors.New("Invalid transaction output program hash")
		}
		if txn.Outputs[payloadObj.OutputIndexes[i]].Value < 0 || payloadObj.CrossChainAmounts[i] < 0 ||
			payloadObj.CrossChainAmounts[i] > txn.Outputs[payloadObj.OutputIndexes[i]].Value-Fixed64(config.Parameters.MinCrossChainTxFee) {
			return errors.New("Invalid transaction outputs")
		}
	}

	//check transaction fee
	var totalInput Fixed64
	reference, err := DefaultLedger.Store.GetTxReference(txn)
	if err != nil {
		return errors.New("Invalid transaction inputs")
	}
	for _, v := range reference {
		totalInput += v.Value
	}

	var totalOutput Fixed64
	for _, output := range txn.Outputs {
		totalOutput += output.Value
	}

	if totalInput-totalOutput < Fixed64(config.Parameters.MinCrossChainTxFee) {
		return errors.New("Invalid transaction fee")
	}

	return nil
}

func CheckRegisterAssetTransaction(txn *core.Transaction) error {
	payload, ok := txn.Payload.(*core.PayloadRegisterAsset)
	if !ok {
		return fmt.Errorf("Invalid register asset transaction payload")
	}

	//asset name should be different
	assets := DefaultLedger.Store.GetAssets()
	for _, asset := range assets {
		if asset.Name == payload.Asset.Name {
			return fmt.Errorf("Asset name has been registed")
		}
	}

	//amount and program hash should be same in output and payload
	totalToken := big.NewInt(0)
	for _, output := range txn.Outputs {
		if output.AssetID.IsEqual(payload.Asset.Hash()) {
			if !output.ProgramHash.IsEqual(payload.Controller) {
				return fmt.Errorf("Register asset program hash not same as program hash in payload")
			}
			totalToken.Add(totalToken, &output.TokenValue)
		}
	}
	regAmount := big.NewInt(int64(payload.Amount))
	regAmount.Mul(regAmount, getPrecisionBigInt())

	if totalToken.Cmp(regAmount) != 0 {
		return fmt.Errorf("Invalid register asset amount")
	}

	return nil
}

func getPrecisionBigInt() *big.Int {
	value := big.Int{}
	value.SetString("1000000000000000000", 10)
	return &value
}
