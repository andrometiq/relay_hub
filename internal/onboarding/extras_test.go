package onboarding

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtrasUseMetaDocumentedShape(t *testing.T) {
	prefill := map[string]any{
		"business": map[string]any{"name": "Acme", "email": "a@acme.test", "website": "https://acme.test", "timezone": "UTC+05:30",
			"country": "IN", "street_address1": "1 Demo Street", "street_address2": "", "city": "Sampleville", "state": "Sample State",
			"zip_postal": "000001", "phone": map[string]any{"code": "+91", "number": "9000000000"}},
		"phone": map[string]any{"display_name": "Acme", "category": "APPAREL", "description": "Garments maker"},
	}
	want := `{"featureType":"","sessionInfoVersion":"3","setup":{"business":{"address":{"city":"Sampleville","country":"IN","state":"Sample State","streetAddress1":"1 Demo Street","zipPostal":"000001"},"email":"a@acme.test","name":"Acme","phone":{"code":91,"number":"9000000000"},"timezone":"UTC+05:30","website":"https://acme.test"},"phone":{"category":"APPAREL","description":"Garments maker","displayName":"Acme"}}}`
	got, _ := json.Marshal(extras("new", prefill))
	var gotValue, wantValue any
	json.Unmarshal(got, &gotValue)
	json.Unmarshal([]byte(want), &wantValue)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("extras\n got %s\nwant %s", got, want)
	}
	if coexistence := extras("coexistence", nil); coexistence["featureType"] != "whatsapp_business_app_onboarding" {
		t.Fatalf("coexistence extras %v", coexistence)
	}
}

func TestSetupDropsABadPhoneCodeAndLongText(t *testing.T) {
	business := map[string]any{"name": string(make([]rune, 513)), "phone": map[string]any{"code": "x", "number": "9000000000"}}
	if out := setup(map[string]any{"business": business}); len(out) != 0 {
		t.Fatalf("setup %v", out)
	}
}
