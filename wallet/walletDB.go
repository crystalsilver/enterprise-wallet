package wallet

/*
 * Manages all the addresses and 2 databases (Wallet DB and GUI DB)
 *
 */

import (
	"fmt"
	"os"
	"os/user"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FactomProject/M2GUIWallet/address"
	"github.com/FactomProject/M2GUIWallet/wallet/database"
	"github.com/FactomProject/factom"
	"github.com/FactomProject/factom/wallet"
	"github.com/FactomProject/factomd/common/primitives"
	// "github.com/FactomProject/factom/wallet/wsapi"
	"encoding/json"
	"github.com/FactomProject/factomd/common/interfaces"
	"github.com/FactomProject/factomd/database/mapdb"
)

const (
	MAP int = iota
	LDB
	BOLT
)

var (
	GUI_DB    = MAP
	WALLET_DB = MAP
	TX_DB     = MAP
)

// Wallet interacting with LDB and factom/wallet
//   The LDB doesn't need to be updated often, so we save after every add and only
//   deal with cached version
type WalletDB struct {
	GUIlDB        interfaces.IDatabase      //database.IDatabase        // GUI DB
	guiWallet     *WalletStruct             // Cached version on GUI LDB
	Wallet        *wallet.Wallet            // Wallet from factom/wallet
	TransactionDB *wallet.TXDatabaseOverlay // Used to display transactions

	// Used to cache related transactions
	// This is rebuilt upon every launch
	relatedTransactionLock sync.RWMutex                  // For all variables associated with related transaction caching
	cachedTransactions     []DisplayTransaction          // All sorted transactions already found
	cachedHeight           uint32                        // Last FBlock height used
	transMap               map[string]DisplayTransaction // Prevent duplicate transactions
	addrMap                map[string]string             // Find addresses quick, All addresses already searched for up to last FBlock
}

// For now is same as New
func LoadWalletDB() (*WalletDB, error) {
	return NewWalletDB()
}

func NewWalletDB() (*WalletDB, error) {
	w := new(WalletDB)

	var db interfaces.IDatabase
	var err error
	switch GUI_DB { // Decides type of wallet DB
	case MAP:
		db, err = database.NewMapDB()
	case LDB:
		db, err = database.NewOrOpenLevelDBWallet(GetHomeDir() + guiLDBPath)
	case BOLT:
		db, err = database.NewOrOpenBoltDBWallet(GetHomeDir() + guiBoltPath)
	}
	if err != nil {
		return nil, err
	}

	w.GUIlDB = db

	// Adds Wallet
	w.guiWallet = NewWallet()
	data, err := w.GUIlDB.Get([]byte("gui-wallet"), []byte("wallet"), new(WalletStruct))
	if err != nil || data == nil {
		err = w.GUIlDB.Put([]byte("gui-wallet"), []byte("wallet"), w.guiWallet)
		if err != nil {
			return nil, err
		}
	} else {
		w.guiWallet = data.(*WalletStruct)
	}

	var wal *wallet.Wallet
	switch WALLET_DB { // Decides type of wallet DB
	case MAP:
		wal, err = wallet.NewMapDBWallet()
	case LDB:
		wal, err = wallet.NewOrOpenLevelDBWallet(GetHomeDir() + walletLDBPath)
	case BOLT:
		wal, err = wallet.NewOrOpenBoltDBWallet(GetHomeDir() + walletBoltPath)
	}
	if err != nil {
		return nil, err
	}

	w.Wallet = wal

	var txdb *wallet.TXDatabaseOverlay
	switch TX_DB {
	case MAP:
		txdb = wallet.NewTXOverlay(new(mapdb.MapDB))
		err = nil
	case LDB:
		txdb, err = wallet.NewTXLevelDB(GetHomeDir() + txdbLDBPath)
	case BOLT:
		txdb, err = wallet.NewTXBoltDB(GetHomeDir() + txdbBoltPath)
	}

	if err != nil {
		return nil, fmt.Errorf("Could not add transaction database to wallet:", err)
	}

	w.Wallet.AddTXDB(txdb)

	w.TransactionDB = w.Wallet.TXDB()
	if w.TransactionDB != nil { // Update DB
		//w.TransactionDB.GetAllTXs()
	}

	err = w.UpdateGUIDB()
	if err != nil {
		return nil, err
	}

	w.transMap = make(map[string]DisplayTransaction)
	w.addrMap = make(map[string]string)
	w.cachedHeight = 0

	return w, nil
}

type TransactionAddressInfo struct {
	Name    string
	Address string
	Amount  uint64
	Type    string // FCT or EC
}

func NewTransactionAddressInfo(name string, address string, amount uint64, tokenType string) *TransactionAddressInfo {
	t := new(TransactionAddressInfo)
	t.Name = name
	t.Address = address
	t.Amount = amount
	t.Type = tokenType

	return t
}

func (a *TransactionAddressInfo) IsSameAs(b TransactionAddressInfo) bool {
	if a.Name != b.Name {
		return false
	}
	if a.Address != b.Address {
		return false
	}
	if a.Amount != b.Amount {
		return false
	}
	if a.Type != b.Type {
		return false
	}
	return true
}

// Names are "" if not in wallet
type DisplayTransaction struct {
	Inputs     []TransactionAddressInfo
	TotalInput uint64

	Outputs        []TransactionAddressInfo
	TotalFCTOutput uint64
	TotalECOutput  uint64

	TxID      string
	Height    uint32
	Action    [3]bool // Sent, recieved, converted
	Date      string
	Time      string
	ExactTime time.Time

	//ITrans interfaces.ITransaction
}

func (a *DisplayTransaction) IsSameAs(b DisplayTransaction) bool {
	if !a.IsSimilarTo(b) {
		return false
	}

	for i := 0; i < 3; i++ {
		if a.Action[i] != b.Action[i] {
			return false
		}
	}

	return true
}

// Does not count actions
func (a *DisplayTransaction) IsSimilarTo(b DisplayTransaction) bool {
	if len(a.Inputs) != len(b.Inputs) {
		return false
	}
	if len(a.Outputs) != len(b.Outputs) {
		return false
	}

	for i := 0; i < len(a.Inputs); i++ {
		if !a.Inputs[i].IsSameAs(b.Inputs[i]) {
			return false
		}
	}
	if a.TotalInput != b.TotalInput {
		return false
	}
	for i := 0; i < len(a.Outputs); i++ {
		if !a.Outputs[i].IsSameAs(b.Outputs[i]) {
			return false
		}
	}
	if a.TotalFCTOutput != b.TotalFCTOutput {
		return false
	}
	if a.TotalECOutput != b.TotalECOutput {
		return false
	}
	if a.TxID != b.TxID {
		return false
	}
	if a.Height != b.Height {
		return false
	}

	return true
}

// for sorting
type DisplayTransactions []DisplayTransaction

func (slice DisplayTransactions) Len() int {
	return len(slice)
}

func (slice DisplayTransactions) Less(i, j int) bool {
	return slice[i].ExactTime.Before(slice[j].ExactTime)
}

func (slice DisplayTransactions) Swap(i, j int) {
	slice[i], slice[j] = slice[j], slice[i]
}

func (slice DisplayTransactions) IsSameAs(comp DisplayTransactions) bool {
	for i := 0; i < slice.Len(); i++ {
		if !slice[i].IsSameAs(comp[i]) {
			return false
		}
	}
	return true
}

func (slice DisplayTransactions) IsSimilarTo(comp DisplayTransactions) bool {
	for i := 0; i < slice.Len(); i++ {
		if !slice[i].IsSimilarTo(comp[i]) {
			return false
		}
	}
	return true
}

func (w *WalletDB) NewDisplayTransaction(t interfaces.ITransaction) (*DisplayTransaction, error) {
	if t == nil {
		return nil, fmt.Errorf("Transaction is nil")
	}

	_, err := w.TransactionDB.GetAllTXs()
	if err != nil {
		return nil, err
	}

	dt := new(DisplayTransaction)
	//dt.ITrans = t
	dt.TotalInput = 0
	dt.TotalFCTOutput = 0
	dt.TotalECOutput = 0
	dt.Height = t.GetBlockHeight()
	dt.TxID = t.GetSigHash().String()
	dt.Inputs = make([]TransactionAddressInfo, 0)
	dt.Outputs = make([]TransactionAddressInfo, 0)
	dt.Action = [3]bool{false, false, false}
	dt.Date = t.GetTimestamp().GetTime().Format(("01/02/2006"))
	dt.Time = t.GetTimestamp().GetTime().Format(("15:04:05"))
	dt.ExactTime = t.GetTimestamp().GetTime()

	ins := t.GetInputs()
	// Inputs
	for _, in := range ins {
		add := primitives.ConvertFctAddressToUserStr(in.GetAddress())
		anp, _ := w.GetGUIAddress(add)
		name := ""
		if anp != nil {
			name = anp.Name
			dt.Action[0] = true
		}

		amt := in.GetAmount()
		dt.TotalInput += amt

		dt.Inputs = append(dt.Inputs, *NewTransactionAddressInfo(name, add, amt, "FCT"))
	}

	outs := t.GetOutputs()
	// FCT Outputs
	for _, out := range outs {
		add := primitives.ConvertFctAddressToUserStr(out.GetAddress())
		anp, _ := w.GetGUIAddress(add)
		name := ""
		if anp != nil {
			name = anp.Name
			dt.Action[1] = true
		}

		amt := out.GetAmount()
		dt.TotalFCTOutput += amt

		dt.Outputs = append(dt.Outputs, *NewTransactionAddressInfo(name, add, amt, "FCT"))
	}

	ecOuts := t.GetECOutputs()
	// EC Outputs
	for _, ecOut := range ecOuts {
		add := primitives.ConvertECAddressToUserStr(ecOut.GetAddress())
		anp, _ := w.GetGUIAddress(add)
		name := ""
		if anp != nil {
			name = anp.Name
			dt.Action[2] = true
		}

		amt := ecOut.GetAmount()
		dt.TotalECOutput += amt

		dt.Outputs = append(dt.Outputs, *NewTransactionAddressInfo(name, add, amt, "EC"))
	}

	return dt, nil
}

// This function grabs all transactions related to any address in the address book
// and sorts them by time.Time. If a new address is added, this will grab all transactions
// from that new address and insert them.
func (w *WalletDB) GetRelatedTransactions() ([]DisplayTransaction, error) {
	w.relatedTransactionLock.Lock()
	defer w.relatedTransactionLock.Unlock()

	// Get current Fblock height
	var err error
	var i int
	var block interfaces.IFBlock
	for i = 0; i < 2; i++ { // 2 tries, if fails first, updates transactions and trys again
		block, err = w.TransactionDB.DBO.FetchFBlockHead()
		if err != nil {
			return nil, err
		}
		if block == nil {
			if i == 0 {
				w.TransactionDB.GetAllTXs()
			} else {
				return nil, fmt.Errorf("Error with loading transaction database.")
			}
		} else {
			break
		}
	}

	var oldHeight uint32
	if block != nil {
		oldHeight = w.cachedHeight
		w.cachedHeight = block.GetDatabaseHeight()
	} else {
		w.TransactionDB.GetAllTXs() // UpdateDB for next attempt if user tries again
		return nil, fmt.Errorf("Error with loading transaction database.")
	}

	// Get all new transaction to go through
	transactions, err := w.TransactionDB.GetTXRange(int(oldHeight), int(w.cachedHeight))
	if err != nil {
		return nil, err
	}

	var newTransactions []DisplayTransaction
	// Sort throught new transactions for any related
	for _, trans := range transactions {
		added := false
		for i = 0; i < 3; i++ {
			var addresses []string
			switch i {
			case 0:
				addrs := trans.GetInputs()
				for _, a := range addrs {
					addresses = append(addresses, primitives.ConvertFctAddressToUserStr(a.GetAddress()))
				}
			case 1:
				addrs := trans.GetOutputs()
				for _, a := range addrs {
					addresses = append(addresses, primitives.ConvertFctAddressToUserStr(a.GetAddress()))
				}
			case 2:
				addrs := trans.GetECOutputs()
				for _, a := range addrs {
					addresses = append(addresses, primitives.ConvertECAddressToUserStr(a.GetAddress()))
				}
			}

			for _, addr := range addresses { // If it makes through this loop will check next set of addresses
				_, ok := w.addrMap[addr]
				if ok {
					dt, err := w.NewDisplayTransaction(trans)
					if err != nil {
						break // Error with transaction
					}

					_, ok := w.transMap[dt.TxID]
					if !ok {
						newTransactions = append(newTransactions, *dt)
						w.transMap[dt.TxID] = *dt
					}
					added = true
					break // Transaction added
				}
			}

			if added {
				break // Transaction added, break out of this transaction
			}
		}
	}

	// Sort the new ones
	sort.Sort(DisplayTransactions(newTransactions))
	// Prepend them to the old cache
	w.cachedTransactions = append(newTransactions, w.cachedTransactions...)

	// Find all new addresses, need to do additional handling and inserting
	var moreTransactions []DisplayTransaction
	anps := w.GetAllGUIAddresses()
	var newAddrs []string
	for _, a := range anps {
		addr, ok := w.addrMap[a.Address]
		if ok || len(addr) > 1 { // Found

		} else { // New addr
			w.addrMap[a.Address] = a.Address
			newAddrs = append(newAddrs, a.Address)
			trans, err := w.TransactionDB.GetTXAddress(a.Address)
			if err == nil {
				if len(trans) > 0 {
					for _, t := range trans {
						dt, _ := w.NewDisplayTransaction(t)
						moreTransactions = append(moreTransactions, *dt)
					}
					//moreTransactions = append(moreTransactions, trans...)
				}
			}
		}
	}

	/* This to end of function breaks the attempt to build for windows for some reason */
	// Binary search and insert new transactions from new addresses
	for _, t := range moreTransactions {
		if _, ok := w.transMap[t.TxID]; ok {
			continue
		}

		i = w.findTransactionIndex(t)

		if i < len(w.cachedTransactions) && w.cachedTransactions[i].TxID == t.TxID {
			// t is present at w.cachedTransactions[i], already there. We need to update the 'Actions'
			// field. If we have the same transaction as before, we don't need to add a new one, but update
			// the existing
			for counter := 0; counter < 3; counter++ {
				// If one or other is true, we want to keep that
				w.cachedTransactions[i].Action[counter] = w.cachedTransactions[i].Action[counter] || t.Action[counter]
			}
		} else {
			// t is not present in w.cachedTransactions,
			// but i is the index where it would be inserted.
			w.transMap[t.TxID] = t // Add to cache
			// Insert
			w.cachedTransactions = append(w.cachedTransactions[:i], append([]DisplayTransaction{t}, w.cachedTransactions[i:]...)...)
		}
	}

	return w.cachedTransactions, nil
}

// Binary search
func (w *WalletDB) findTransactionIndex(t DisplayTransaction) int {
	low := 0
	high := len(w.cachedTransactions) - 1

	for low <= high {
		mid := low + ((high - low) / 2)
		if w.cachedTransactions[mid].TxID == t.TxID {
			return mid
		}
		if !w.cachedTransactions[mid].ExactTime.Before(t.ExactTime) {
			high = mid - 1
		} else {
			low = mid + 1
		}
	}

	return low
}

// No cache solution, not going to use it. It is too slow, but was used in early phases and kept
// for testing comparisons as this should be all inclusive and correct
func (w *WalletDB) GetRelatedTransactionsNoCaching() ([]DisplayTransaction, error) {
	// ## No cache solution ##
	transMap := make(map[string]interfaces.ITransaction)
	var transList []DisplayTransaction
	adds := w.GetAllGUIAddresses()
	for _, a := range adds {
		transactions, err := w.TransactionDB.GetTXAddress(a.Address)
		if err != nil {
			return nil, err
		}

		for _, trans := range transactions {
			i, _ := transMap[trans.GetHash().String()]
			if i == nil {
				transMap[trans.GetHash().String()] = trans
				dt, err := w.NewDisplayTransaction(trans)
				if err != nil {
					return nil, err
				}
				transList = append(transList, *dt)
			}
		}
	}

	sort.Sort(DisplayTransactions(transList))
	return transList, nil
}

func (w *WalletDB) GetGUIWalletJSON() ([]byte, error) {
	w.AddBalancesToAddresses()
	return json.Marshal(w.guiWallet)
}

func (w *WalletDB) AddBalancesToAddresses() {
	w.guiWallet.AddBalancesToAddresses()
}

// Grabs the list of addresses from the walletDB and updates our GUI
// with any that are missing. All will be external
func (w *WalletDB) UpdateGUIDB() error {
	faAdds, ecAdds, err := w.Wallet.GetAllAddresses()
	if err != nil {
		return err
	}

	var addMap map[string]string
	addMap = make(map[string]string)

	var names []string
	var addresses []string

	guiAdds := w.GetAllGUIAddresses()

	// Add addresses to GUI from cli
	for _, fa := range faAdds {
		_, list := w.GetGUIAddress(fa.String())
		if list == -1 {
			names = append(names, "FA-Imported-From-CLI")
			addresses = append(addresses, fa.String())
		}
		addMap[fa.String()] = fa.String()
	}

	for _, ec := range ecAdds {
		_, list := w.GetGUIAddress(ec.String())
		if list == -1 {
			names = append(names, "EC-Imported-From-CLI")
			addresses = append(addresses, ec.String())
		}
		addMap[ec.String()] = ec.String()
	}

	// Add in new guys
	if len(names) > 0 {
		err = w.addBatchGUIAddresses(names, addresses)
		if err != nil {
			return err
		}
	}

	// Missing from CLI? We need to remove them here
	for _, guiAdd := range guiAdds {
		if _, ok := addMap[guiAdd.Address]; !ok {
			w.RemoveAddress(guiAdd.Address)
		}
	}

	return w.Save()
}

func (w *WalletDB) Close() error {
	// Combine all close errors, as all need to get closed
	errCount := 0
	errString := ""

	err := w.Save()
	if err != nil {
		errCount++
		errString = errString + "; " + err.Error()
	}
	err = w.Wallet.Close()
	if err != nil {
		errCount++
		errString = errString + "; " + err.Error()
	}
	err = w.GUIlDB.Close()
	if err != nil {
		errCount++
		errString = errString + "; " + err.Error()
	}

	err = w.TransactionDB.Close()
	if err != nil {
		errCount++
		errString = errString + "; " + err.Error()
	}

	if errCount > 0 {
		return fmt.Errorf("Found %d errors: %s", errCount, errString)
	}
	return nil
}

func (w *WalletDB) Save() error {
	err := w.GUIlDB.Put([]byte("gui-wallet"), []byte("wallet"), w.guiWallet)
	if err != nil {
		return err
	}

	return nil
}

func (w *WalletDB) GenerateFactoidAddress(name string) (*address.AddressNamePair, error) {
	address, err := w.Wallet.GenerateFCTAddress()

	if err != nil {
		return nil, err
	}

	anp, err := w.guiWallet.AddAddress(name, address.String(), 1)
	if err != nil {
		return nil, err
	}

	err = w.Save()
	if err != nil {
		return nil, err
	}
	return anp, nil
}

func (w *WalletDB) GetPrivateKey(address string) (secret string, err error) {
	if !factom.IsValidAddress(address) {
		return "", fmt.Errorf("Not a valid address")
	}

	if address[:2] == "FA" {
		return w.getFCTPrivateKey(address)
	} else if address[:2] == "EC" {
		return w.getECPrivateKey(address)
	}

	return "", fmt.Errorf("Not a public address")
}

func (w *WalletDB) getECPrivateKey(address string) (secret string, err error) {
	adds, err := w.Wallet.GetAllECAddresses()
	if err != nil {
		return "", err
	}

	for _, ec := range adds {
		if strings.Compare(ec.String(), address) == 0 {
			return ec.SecString(), nil
		}
	}

	return "", fmt.Errorf("Address not found")
}

func (w *WalletDB) getFCTPrivateKey(address string) (secret string, err error) {
	adds, err := w.Wallet.GetAllFCTAddresses()
	if err != nil {
		return "", err
	}

	for _, fa := range adds {
		if strings.Compare(fa.String(), address) == 0 {
			return fa.SecString(), nil
		}
	}

	return "", fmt.Errorf("Address not found")
}

func (w *WalletDB) GenerateEntryCreditAddress(name string) (*address.AddressNamePair, error) {
	address, err := w.Wallet.GenerateECAddress()
	if err != nil {
		return nil, err
	}

	anp, err := w.guiWallet.AddAddress(name, address.String(), 2)
	if err != nil {
		return nil, err
	}

	w.Save()
	if err != nil {
		return nil, err
	}

	return anp, nil
}

// TODO: Fix, make wallet take the remove
func (w *WalletDB) RemoveAddress(address string) (*address.AddressNamePair, error) {
	anp, err := w.guiWallet.RemoveAddress(address)
	if err != nil {
		return nil, err
	}

	err = w.Save()
	if err != nil {
		return nil, err
	}

	return anp, nil
}

func (w *WalletDB) AddAddress(name string, secret string) (*address.AddressNamePair, error) {
	if !factom.IsValidAddress(secret) {
		return nil, fmt.Errorf("Not a valid private key")
	} else if secret[:2] == "Fs" {
		add, err := factom.GetFactoidAddress(secret)
		if err != nil {
			return nil, err
		}

		err = w.Wallet.InsertFCTAddress(add)
		if err != nil {
			return nil, err
		}

		anp, err := w.addGUIAddress(name, add.String())
		if err != nil {
			return nil, err
		}

		err = w.Save()
		if err != nil {
			return nil, err
		}

		return anp, nil
	} else if secret[:2] == "Es" {
		add, err := factom.GetECAddress(secret)
		if err != nil {
			return nil, err
		}

		err = w.Wallet.InsertECAddress(add)
		if err != nil {
			return nil, err
		}

		anp, err := w.addGUIAddress(name, add.String())
		if err != nil {
			return nil, err
		}

		err = w.Save()
		if err != nil {
			return nil, err
		}

		return anp, nil
	}
	return nil, fmt.Errorf("Not a valid private key")
}

// Only adds to GUI Database
func (w *WalletDB) addBatchGUIAddresses(names []string, addresses []string) error {
	if len(names) != len(addresses) {
		return fmt.Errorf("List length does not match")
	}

	for i := 0; i < len(names); i++ {
		w.addGUIAddress(names[i], addresses[i])
	}

	return w.Save()
}

// Only adds to GUI database
func (w *WalletDB) addGUIAddress(name string, address string) (*address.AddressNamePair, error) {
	anp, err := w.guiWallet.AddAddress(name, address, 3)
	if err != nil {
		return nil, err
	}
	err = w.Save()
	if err != nil {
		return nil, err
	}

	return anp, nil
}

// Returns address with associated name
// List is 0 for not found, 1 for Factoid address, 2 for EC Address, 3 for External
func (w *WalletDB) GetGUIAddress(address string) (anp *address.AddressNamePair, list int) {
	anp, list, _ = w.guiWallet.GetAddress(address)
	return
}

func (w *WalletDB) ChangeAddressName(address string, toName string) error {
	err := w.guiWallet.ChangeAddressName(address, toName)
	if err != nil {
		return err
	}
	return w.Save()
}

func (w *WalletDB) GetTotalGUIAddresses() uint32 {
	return w.guiWallet.GetTotalAddressCount()
}

func (w *WalletDB) GetAllGUIAddresses() []address.AddressNamePair {
	return w.guiWallet.GetAllAddresses()
}

func (w *WalletDB) IsValidAddress(address string) bool {
	return factom.IsValidAddress(address)
}

func (w *WalletDB) GetECBalance() int64 {
	return w.guiWallet.ECTotal
}

func (w *WalletDB) GetFactoidBalance() int64 {
	return w.guiWallet.FactoidTotal
}

func GetHomeDir() string {
	// Get the OS specific home directory via the Go standard lib.
	var homeDir string
	usr, err := user.Current()
	if err == nil {
		homeDir = usr.HomeDir
	}

	// Fall back to standard HOME environment variable that works
	// for most POSIX OSes if the directory from the Go standard
	// lib failed.
	if err != nil || homeDir == "" {
		homeDir = os.Getenv("HOME")
	}
	return homeDir
}
