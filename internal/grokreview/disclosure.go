package grokreview

import "fmt"

const ReviewDisclosureSchema = "squad.review-disclosure.v1"

type DisclosureReadback struct {
	Status             string `json:"status"`
	Reference          string `json:"reference,omitempty"`
	ReceiptDisposition string `json:"receipt_disposition,omitempty"`
	Sampled            bool   `json:"sampled"`
	Published          bool   `json:"published"`
}

func disclosureMatches(receipt, request ReviewDisclosure) bool {
	return receipt.SchemaVersion == ReviewDisclosureSchema && receipt.Reference != "" && receipt.Owner == request.Owner && receipt.Identity == request.Identity && receipt.Mode == request.Mode && receipt.Operation == request.Operation && receipt.Provider == "grok" && request.Provider == "grok" && receipt.Content == "source_diff_and_review_contract" && receipt.Content == request.Content
}

// ReadDisclosure qualifies an existing operator-authored scope receipt. It does
// not create permission, ask again, sample, publish or authenticate a provider.
func ReadDisclosure(receiptPath, requestPath string, owner ReviewOwner) (DisclosureReadback, error) {
	var request ReviewDisclosure
	if _, err := readBoundedJSON(requestPath, &request); err != nil {
		return DisclosureReadback{}, err
	}
	if request.SchemaVersion != ReviewDisclosureSchema || request.Provider != "grok" || request.Content != "source_diff_and_review_contract" || request.Owner != owner || owner.Actor == "" || owner.Native == "" || request.Identity.Repository == "" || request.Identity.PR <= 0 || request.Identity.BaseRef == "" || request.Identity.BaseSHA == "" || request.Identity.HeadSHA == "" || (request.Operation != "managed_review" && request.Operation != "prospective_legacy_readmission") || (request.Mode != "shadow" && request.Mode != "required") {
		return DisclosureReadback{}, fmt.Errorf("exact current recipient/review scope required")
	}
	if receiptPath == "" {
		return DisclosureReadback{Status: "unavailable"}, nil
	}
	var receipt ReviewDisclosure
	if _, err := readBoundedJSON(receiptPath, &receipt); err != nil {
		return DisclosureReadback{}, err
	}
	result := DisclosureReadback{Status: "scope-mismatch", Reference: receipt.Reference}
	if receipt.Disposition == "granted" || receipt.Disposition == "pending" || receipt.Disposition == "denied" {
		result.ReceiptDisposition = receipt.Disposition
	}
	if !disclosureMatches(receipt, request) {
		return result, nil
	}
	switch receipt.Disposition {
	case "granted", "pending", "denied":
		result.Status = receipt.Disposition
	default:
		result.Status = "unavailable"
	}
	return result, nil
}
