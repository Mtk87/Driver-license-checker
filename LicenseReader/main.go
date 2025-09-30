package main

import (
	//"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"io"
	"regexp"
	"strings"
	"syscall"
	"golang.org/x/term"
	"time"
)

const scannedFile = "scanned.json" // file lives next to the executable

var scannedCounts map[string]int // licenseNumber → how many times we saw it


// ---------------------------------------------------------------
// 1️⃣  Public data structure – what callers will receive
// ---------------------------------------------------------------
type DriverLicense struct {
	FirstName      string `json:"first_name,omitempty"`
	MiddleName     string `json:"middle_name,omitempty"`
	LastName       string `json:"last_name,omitempty"`
	Suffix         string `json:"suffix,omitempty"` // e.g. Jr., Sr., III
	FullName       string `json:"full_name,omitempty"` // "LAST, FIRST M. Suffix"
	LicenseNumber  string `json:"license_number,omitempty"`

	IssueDate      string `json:"issue_date,omitempty"`      // YYYYMMDD
	ExpirationDate string `json:"expiration_date,omitempty"` // YYYYMMDD
	DateOfBirth    string `json:"date_of_birth,omitempty"`   // YYYYMMDD

	// -------- Address fields (expanded) ----------
	StreetLine1    string `json:"street_line1,omitempty"` // DAG
	StreetLine2    string `json:"street_line2,omitempty"` // DAH
	City           string `json:"city,omitempty"`         // DAI
	State          string `json:"state,omitempty"`        // DAJ
	PostalCode     string `json:"postal_code,omitempty"`   // DAK (ZIP+4 may be split)
	PostalCodeExt  string `json:"postal_code_ext,omitempty"` // optional 4‑digit extension (if present)
	Country        string `json:"country,omitempty"`      // DCF (rare, but defined)

	// -------- Miscellaneous ----------
	Sex            string `json:"sex,omitempty"`          // DBC (1=Male, 2=Female, 9=Unspecified)
	EyeColor       string `json:"eye_color,omitempty"`    // DAY
	Height         string `json:"height,omitempty"`       // DAU (e.g. 180CM)
	WeightKg       string `json:"weight_kg,omitempty"`    // DAW (kilograms)
	VehicleClass   string `json:"vehicle_class,omitempty"`// DCB (e.g. C, D, M)
	Restrictions   string `json:"restrictions,omitempty"` // DCR (e.g. B, C)
	Endorsements   string `json:"endorsements,omitempty"` // DCE (additional endorsements)
	IssuerID       string `json:"issuer_id,omitempty"`    // DDI (state/agency code)

	RawData        string `json:"raw_data,omitempty"`     // original barcode string
}

var Reset = "\033[0m"
var Red = "\033[31m"
var Green = "\033[32m"
var Yellow = "\033[33m"

var elementIDs = []string{
	// Personal name
	"DAC", // First name
	"DAD", // Middle name
	"DCS", // Last name
	"DCE", // Suffix (Jr., Sr., III)
	"DDF", // trash
	"DDG", // trash


	// Licence numbers & dates
	"DAQ", // Licence number
	"DBD", // Issue date (YYYYMMDD)
	"DBA", // Expiration date (YYYYMMDD)
	"DBB", // Date of birth (YYYYMMDD)

	// Address
	"DAG", // Street line 1
	"DAH", // Street line 2
	"DAI", // City
	"DAJ", // State
	"DAK", // Postal code (ZIP or ZIP‑4)
	"DCF", // Country (rare)

	// Misc
	"DBC", // Sex
	"DAY", // Eye color
	"DAU", // Height
	"DAW", // Weight (kg)
	"DCB", // Vehicle class
	"DCR", // Restrictions
	"DDE", // Endorsements (some jurisdictions use DCE, keep both)
	"DDI", // Issuer ID

	// Anything else you might want later can be added here…
}

// ---------------------------------------------------------------
// 3️⃣  Build a regex that ONLY matches the IDs above
// ---------------------------------------------------------------
func buildIDRegex() *regexp.Regexp {
	// Escape each ID (they’re already safe, but good practice)
	escaped := make([]string, len(elementIDs))
	for i, id := range elementIDs {
		escaped[i] = regexp.QuoteMeta(id)
	}
	// Join with "|" to create an alternation group: (?:DAQ|DCS|DAC|...)
	pattern := "(?:" + strings.Join(escaped, "|") + ")" + "([^A-Z]*)"
	return regexp.MustCompile(pattern)
}

// ---------------------------------------------------------------
// 2️⃣  Forward‑scanning AAMVA parser (order‑agnostic)
// ---------------------------------------------------------------
func parseAAMVAForward(raw string) map[string]string {
	// Remove the leading “@” and the 5‑character header (e.g. "ANSI ")
	raw = strings.TrimPrefix(raw, "@")
	if len(raw) > 5 {
		raw = raw[5:]
	}

	// Build the strict regex: first capture = the ID, second capture = its value
	// Example pattern after build:  ((?:DAQ|DCS|DAC|…))([^A-Z]*)
	idPattern := regexp.MustCompile(`((?:` + strings.Join(elementIDs, "|") + `))([^A-Z]*)`)

	// Find all matches. Each match yields indices:
	//   [fullStart fullEnd idStart idEnd valueStart valueEnd ...]
	matches := idPattern.FindAllStringSubmatchIndex(raw, -1)

	result := make(map[string]string)

	for i, loc := range matches {
		// loc[2]..loc[3] -> the three‑letter ID
		id := raw[loc[2]:loc[3]]

		// loc[4] is the start of the value for this ID
		valStart := loc[4]

		// Determine where the value ends:
		//   • If this is the last match, the value runs to the end of the string.
		//   • Otherwise it ends right before the next ID begins (which is at matches[i+1][2]).
		var valEnd int
		if i+1 < len(matches) {
			valEnd = matches[i+1][2] // start index of the next ID
		} else {
			valEnd = len(raw)
		}
		value := raw[valStart:valEnd]

		// Trim trailing control characters (CR, LF, NUL) that sometimes appear.
		value = strings.TrimRight(value, "\r\n\x00")
		result[id] = value
	}
	return result
}

// ---------------------------------------------------------------------
//  Load previously saved scan counts (if the file exists)
// ---------------------------------------------------------------------
func loadScannedCounts() {
	scannedCounts = make(map[string]int)

	f, err := os.Open(scannedFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No file yet – start with an empty map
			return
		}
		log.Fatalf("cannot open %s: %v", scannedFile, err)
	}
	defer f.Close()

	// If the file is empty, just keep the empty map.
	info, _ := f.Stat()
	if info.Size() == 0 {
		return
	}

	dec := json.NewDecoder(f)
	if err := dec.Decode(&scannedCounts); err != nil {
		// An EOF here means the file contained no JSON – treat it as empty.
		if err == io.EOF {
			return
		}
		log.Fatalf("cannot decode %s: %v", scannedFile, err)
	}
}

// ---------------------------------------------------------------------
//  Persist the current map to disk (overwrites the old file)
// ---------------------------------------------------------------------
func saveScannedCounts() {
	tmp := scannedFile + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		log.Printf("warning: could not write scan cache: %v", err)
		return
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(scannedCounts); err != nil {
		log.Printf("warning: could not encode scan cache: %v", err)
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	// Atomically replace the old file
	os.Rename(tmp, scannedFile)
}


// ---------------------------------------------------------------
// 3️⃣  Map → strongly‑typed struct (now with full address handling)
// ---------------------------------------------------------------
func mapToLicense(m map[string]string, raw string) DriverLicense {
	// ----- Names -----
	first := m["DAC"]
	middle := m["DAD"]
	last := m["DCS"]
	suffix := m["DCE"]

	// Build a nice “LAST, FIRST M. Suffix” string
	full := strings.TrimSpace(last + ", " + first)
	if middle != "" && middle != "NONE" {
		full += " " + middle
	}
	if suffix != "" {
		full += " " + suffix
	}

	// ----- Address -----
	street1 := m["DAG"]
	street2 := m["DAH"]
	city := m["DAI"]
	state := m["DAJ"]
	zipFull := m["DAK"] // may be 5‑digit or 9‑digit (ZIP+4)

	postalCode := zipFull
	postalExt := ""
	if len(zipFull) > 5 && strings.Contains(zipFull, "-") {
		parts := strings.SplitN(zipFull, "-", 2)
		postalCode = parts[0]
		postalExt = parts[1]
	} else if len(zipFull) > 5 && len(zipFull) == 9 {
		postalCode = zipFull[:5]
		postalExt = zipFull[5:]
	}

	// Some jurisdictions put the country code in DCF (rare)
	country := m["DCF"]

	return DriverLicense{
		FirstName:      first,
		MiddleName:     middle,
		LastName:       last,
		Suffix:         suffix,
		FullName:       full,
		LicenseNumber:  m["DAQ"],
		IssueDate: 		m["DBD"],
		ExpirationDate: m["DBA"], // YYYYMMDD
		DateOfBirth:    m["DBB"], // YYYYMMDD

		StreetLine1:    street1,
		StreetLine2:    street2,
		City:           city,
		State:          state,
		PostalCode:     postalCode,
		PostalCodeExt:  postalExt,
		Country:        country,

		Sex:            m["DBC"],
		EyeColor:       m["DAY"],
		Height:         m["DAU"],
		WeightKg:       m["DAW"],
		VehicleClass:   m["DCB"],
		Restrictions:   m["DCR"],
		Endorsements:   m["DCE"],
		IssuerID:       m["DDI"],

		RawData: raw,
	}
}

// ---------------------------------------------------------------
// 4️⃣  Main loop – read, parse, display (optional JSON)
// ---------------------------------------------------------------
func main() {
	fmt.Println("=== Driver‑License PDF417 Text Reader (hidden input) ===")
	fmt.Println("Scan the barcode – the raw data will NOT be shown on screen.")
	fmt.Println("Type 'q' (or Ctrl‑C) to quit.")
	fmt.Println("Add '--json' as a command‑line argument to get JSON output.\n")
    fmt.Println("Ready for Scan")
	outputJSON := false
	if len(os.Args) > 1 && os.Args[1] == "--json" {
		outputJSON = true
	}

	//input, err := term.ReadPassword(0)
	//scanner := bufio.NewScanner(input)
	loadScannedCounts() // <-- load the persisted counts
	for {
		
		pine, err := term.ReadPassword(int(syscall.Stdin))
		line := string(pine)
		if err != nil {
			log.Fatalf("input error: %v", err)
		}
		//if !scanner.Scan() {
		//	if err := scanner.Err(); err != nil {
		//		log.Fatalf("input error: %v", err)
		//	}
		//	break
		//}
		//line := strings.TrimSpace(scanner.Text())
		if strings.EqualFold(line, "q") || strings.EqualFold(line, "quit") {
			fmt.Println("Bye!")
			break
		
		}
		if line == "" {
			continue // ignore empty lines
		}

		// ---------------------------------------------------------
		// 4a️⃣  Parse the raw AAMVA string
		// ---------------------------------------------------------
		parsedMap := parseAAMVAForward(line)
		//fmt.Println(parsedMap) debug parsing

		// ---------------------------------------------------------
		// 4b️⃣  Convert map → struct
		// ---------------------------------------------------------
		//license := mapToLicense(parsedMap, line)
		// -------------------------------------------------------------
		// 6️⃣  Scan‑count logic (new)
		// -------------------------------------------------------------
		license := mapToLicense(parsedMap, line)
		if license.LicenseNumber == "" {
			fmt.Println("⚠️  No licence number found – skipping count check.")
		} else {
			cnt := scannedCounts[license.LicenseNumber]

			if cnt >= 2 { // already scanned twice → third time is “too many”
				fmt.Printf("🚫  License %s has been scanned %d times – entry denied.\n",
					license.LicenseNumber, cnt+1)
				// Still persist the increment so the next run knows it’s been seen again
				scannedCounts[license.LicenseNumber] = cnt + 1
				saveScannedCounts()
				// Skip normal output for this scan
				continue
			}

			// Not over the limit – record this scan
			scannedCounts[license.LicenseNumber] = cnt + 1
			saveScannedCounts()
		}

		// LOGIC//
		is21 := false
		age := 0
		if len(license.DateOfBirth) > 0 {
		// Parse using the exact layout; ignore errors – an invalid DOB will stay zero.
			dob, _ := time.Parse("01022006", license.DateOfBirth)
			if !dob.IsZero() {
				now := time.Now()
				age = now.Year() - dob.Year()
				// If birthday hasn't occurred yet this year, subtract one.
				if now.Month() < dob.Month() ||
				(now.Month() == dob.Month() && now.Day() < dob.Day()) {
				age--
			}
				if age >= 21 {
				is21 = true
			}
		}
		}
		isexp := false
		if len(license.ExpirationDate) > 0 {
		// Parse using the exact layout; ignore errors – an invalid
			exp, _ := time.Parse("01022006", license.ExpirationDate)
			if !exp.IsZero() {
				now := time.Now()
				if exp.After(now) {
				isexp = true
				}
			}
		}
		// ---------------------------------------------------------
		// 4c️⃣  Output
		// ---------------------------------------------------------
		if outputJSON {
			enc, err := json.MarshalIndent(license, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "JSON marshal error: %v\n", err)
				continue
			}
			fmt.Println(string(enc)) 
		} else {
			fmt.Println("\n--- Parsed License ---")
			fmt.Printf("Age:%d\n", age)
			fmt.Printf("Full Name       : %s\n", license.FullName)
			fmt.Printf("License Number  : %s\n", license.LicenseNumber)
			if is21 {
				fmt.Printf(Green + "Age             : %d \u2705\n" + Reset, age)
				
			} else {
				fmt.Printf(Red + "Age             : %d 🚫 \n" + Reset, age)
			}
			fmt.Printf("DOB             : %s\n" + Reset, license.DateOfBirth)
			fmt.Printf("Issued          : %s\n", license.IssueDate)
			if isexp {
				fmt.Printf(Green + "Expires         : %s \u2705\n" + Reset, license.ExpirationDate)
				
			} else {
				fmt.Printf(Red + "Expires         : %s 🚫 \n" + Reset, license.ExpirationDate)
			}
			fmt.Println("-----------------------\n")
			fmt.Println("Ready for Scan")
		}
	}

}
