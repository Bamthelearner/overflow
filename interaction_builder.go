package overflow

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/enescakir/emoji"
	"github.com/onflow/cadence"
	"github.com/onflow/flow-cli/pkg/flowkit"
	"github.com/onflow/flow-go-sdk"
	"github.com/pkg/errors"
)

// Flow Interaction Builder
//
// An interaction in overflow is either a script or a transaction

// FlowInteractionBuilder used to create a builder pattern for an interaction
type FlowInteractionBuilder struct {

	//The underlying state of overflow used to fetch some global settings
	Overflow *OverflowState

	//The file name of the interaction
	FileName string

	//The content of the interaction
	Content string

	//The list of raw arguments
	Arguments []cadence.Value

	//TODO: Should this be payer?
	//The main signer used to sign the transaction
	MainSigner *flowkit.Account

	//The propser account
	Proposer *flowkit.Account

	//The payload signers that will sign the payload
	PayloadSigners []*flowkit.Account

	//The gas limit to set for this given interaction
	GasLimit uint64

	//The basepath on where to look for interactions
	BasePath string

	//An error object to store errors that arrive as you configure an interaction
	Error error

	//The code of the tranasction in bytes
	TransactionCode []byte

	//The named arguments
	NamedArgs map[string]interface{}

	//Event filters to apply to the interaction
	EventFilter OverflowEventFilter

	//Wheter to ignore global event filters from OverflowState or not
	IgnoreGlobalEventFilters bool
}

// Deprecated: This builder and all its methods are deprecated. Use the new Tx/Script methods and its argument method
func (f FlowInteractionBuilder) Test(t *testing.T) TransactionResult {
	locale, _ := time.LoadLocation("UTC")
	time.Local = locale
	result := f.Send()
	var formattedEvents []*FormatedEvent
	for _, event := range result.RawEvents {
		ev := ParseEvent(event, uint64(0), time.Unix(0, 0), []string{})
		formattedEvents = append(formattedEvents, ev)
	}
	return TransactionResult{
		Err:     result.Err,
		Events:  formattedEvents,
		Result:  result,
		Testing: t,
	}
}

// get the contract code
func (t FlowInteractionBuilder) getContractCode(codeFileName string) ([]byte, error) {
	code := []byte(t.Content)
	var err error
	if t.Content == "" {
		code, err = t.Overflow.State.ReaderWriter().ReadFile(codeFileName)
		if err != nil {
			return nil, fmt.Errorf("%v Could not read interaction file from path=%s", emoji.PileOfPoo, codeFileName)
		}
	}
	return code, nil
}

//A function to customize the transaction builder
type InteractionOption func(*FlowInteractionBuilder)

// set a list of args as key, value in an interaction, see Arg for options you can pass in
func Args(args ...interface{}) InteractionOption {

	return func(ftb *FlowInteractionBuilder) {
		if len(args)%2 != 0 {
			ftb.Error = fmt.Errorf("Please send in an even number of string : interface{} pairs")
			return
		}
		var i = 0
		for i < len(args) {
			key := args[0]
			value, labelOk := key.(string)
			if !labelOk {
				ftb.Error = fmt.Errorf("even parameters in Args needs to be strings")
			}
			ftb.NamedArgs[value] = args[1]
			i = i + 2
		}
	}
}

// set arguments to the interaction from a map. See Arg for options on what you can pass in
func ArgsM(args map[string]interface{}) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		for key, value := range args {
			ftb.NamedArgs[key] = value
		}
	}
}

// Send an argument into a transaction
//
// The value is treated in the given way depending on type
// - cadence.Value is sent as straight argument
// - string argument are resolved into cadence.Value using flowkit
// - ofther values are converted to string with %v and resolved into cadence.Value using flowkit
// - if the type of the paramter is Address and the string you send in is a valid account in flow.json it will resolve
func Arg(name string, value interface{}) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		ftb.NamedArgs[name] = value
	}
}

// sending in a timestamp as an arg is quite complicated, use this method with the name of the arg, the datestring and the given timezone to parse it at
func DateTimeArg(name string, dateString string, timezone string) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		value, err := parseTime(dateString, timezone)
		if err != nil {
			ftb.Error = err
			return
		}

		//swallow the error since it will never happen here, we control the input
		amount, _ := cadence.NewUFix64(value)

		ftb.NamedArgs[name] = amount
	}
}

// Send in an array of addresses as an argument
func Addresses(name string, value ...string) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		array := []cadence.Value{}

		for _, val := range value {
			account, err := ftb.Overflow.AccountE(val)
			if err != nil {
				address, err := HexToAddress(val)
				if err != nil {
					ftb.Error = errors.Wrap(err, fmt.Sprintf("%s is not an valid account name or an address", val))
					return
				}
				cadenceAddress := cadence.BytesToAddress(address.Bytes())
				array = append(array, cadenceAddress)
			} else {
				cadenceAddress := cadence.BytesToAddress(account.Address().Bytes())
				array = append(array, cadenceAddress)
			}
		}
		ftb.NamedArgs[name] = cadence.NewArray(array)
	}
}

// set the proposer
func ProposeAs(proposer string) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		account, err := ftb.Overflow.AccountE(proposer)
		if err != nil {
			ftb.Error = err
			return
		}
		ftb.Proposer = account
	}
}

// set the propser to be the service account
func ProposeAsServiceAccount() InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		key := ftb.Overflow.ServiceAccountName()
		account, _ := ftb.Overflow.State.Accounts().ByName(key)
		ftb.Proposer = account
	}
}

// set payer, proposer authorizer as the signer
func SignProposeAndPayAs(signer string) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		account, err := ftb.Overflow.AccountE(signer)
		if err != nil {
			ftb.Error = err
			return
		}
		ftb.MainSigner = account
		ftb.Proposer = account
	}
}

// set service account as payer, proposer, authorizer
func SignProposeAndPayAsServiceAccount() InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		key := ftb.Overflow.ServiceAccountName()
		account, _ := ftb.Overflow.State.Accounts().ByName(key)
		ftb.MainSigner = account
		ftb.Proposer = account
	}
}

// set the gas limit
func Gas(gas uint64) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		ftb.GasLimit = gas
	}
}

// set a filter for events
func EventFilter(filter OverflowEventFilter) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		ftb.EventFilter = filter
	}
}

// ignore global events filters defined on OverflowState
func IgnoreGlobalEventFilters() InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		ftb.IgnoreGlobalEventFilters = true
	}
}

// set an aditional authorizer that will sign the payload
func PayloadSigner(signer ...string) InteractionOption {
	return func(ftb *FlowInteractionBuilder) {
		for _, signer := range signer {
			account, err := ftb.Overflow.AccountE(signer)
			if err != nil {
				ftb.Error = err
				return
			}
			ftb.PayloadSigners = append(ftb.PayloadSigners, account)
		}
	}
}

// Send a intereaction builder as a Transaction returning an overflow result
func (t FlowInteractionBuilder) Send() *OverflowResult {
	result := &OverflowResult{StopOnError: t.Overflow.StopOnError}
	if t.Error != nil {
		result.Err = t.Error
		return result
	}

	if t.Proposer == nil {
		result.Err = fmt.Errorf("%v You need to set the main signer", emoji.PileOfPoo)
		return result
	}

	codeFileName := fmt.Sprintf("%s/%s.cdc", t.BasePath, t.FileName)

	if len(t.TransactionCode) == 0 {
		code, err := t.getContractCode(codeFileName)
		if err != nil {
			result.Err = err
			return result
		}
		t.TransactionCode = code
	}

	t.Overflow.Log.Reset()
	t.Overflow.EmulatorLog.Reset()
	// we append the mainSigners at the end here so that it signs last
	signers := t.PayloadSigners
	if t.MainSigner != nil {
		signers = append(signers, t.MainSigner)
	}

	var authorizers []flow.Address
	for _, signer := range signers {
		authorizers = append(authorizers, signer.Address())
	}
	if t.MainSigner == nil {
		signers = append(signers, t.Proposer)
	}

	tx, err := t.Overflow.Services.Transactions.Build(
		t.Proposer.Address(),
		authorizers,
		t.Proposer.Address(),
		t.Proposer.Key().Index(),
		t.TransactionCode,
		codeFileName,
		t.GasLimit,
		t.Arguments,
		t.Overflow.Network,
		true,
	)
	if err != nil {
		result.Err = err
		return result
	}

	for _, signer := range signers {
		err = tx.SetSigner(signer)
		if err != nil {
			result.Err = err
			return result
		}

		tx, err = tx.Sign()
		if err != nil {
			result.Err = err
			return result
		}
	}
	txId := tx.FlowTransaction().ID()
	result.Id = txId

	txBytes := []byte(fmt.Sprintf("%x", tx.FlowTransaction().Encode()))
	ftx, res, err := t.Overflow.Services.Transactions.SendSigned(txBytes, true)
	result.Transaction = ftx

	if err != nil {
		result.Err = err
		return result
	}

	logMessage, err := t.Overflow.readLog()
	if err != nil {
		result.Err = err
	}
	result.RawLog = logMessage

	result.Meter = &Meter{}
	var meter Meter
	scanner := bufio.NewScanner(t.Overflow.EmulatorLog)
	for scanner.Scan() {
		txt := scanner.Text()
		if strings.Contains(txt, "transaction execution data") {
			err = json.Unmarshal([]byte(txt), &meter)
			if err == nil {
				result.Meter = &meter
			}
		}
	}
	messages := []string{}
	for _, msg := range logMessage {
		if msg.ComputationUsed != 0 {
			result.ComputationUsed = msg.ComputationUsed
		}
		messages = append(messages, msg.Msg)
	}

	result.EmulatorLog = messages

	result.RawEvents = res.Events

	overflowEvents, fee := ParseEvents(result.RawEvents)
	result.Fee = fee
	if !t.IgnoreGlobalEventFilters {

		fee := result.Fee["amount"]
		if t.Overflow.FilterOutFeeEvents && fee != nil {
			overflowEvents = overflowEvents.FilterFees(fee.(float64))
		}

		if t.Overflow.FilterOutEmptyWithDrawDepositEvents {
			overflowEvents = overflowEvents.FilterTempWithdrawDeposit()
		}

		if len(t.Overflow.GlobalEventFilter) != 0 {
			overflowEvents = overflowEvents.FilterEvents(t.Overflow.GlobalEventFilter)
		}
	}

	if len(t.EventFilter) != 0 {
		overflowEvents = overflowEvents.FilterEvents(t.EventFilter)
	}

	result.Events = overflowEvents

	result.Name = t.FileName
	t.Overflow.Log.Reset()
	t.Overflow.EmulatorLog.Reset()
	result.Err = res.Error
	return result
}