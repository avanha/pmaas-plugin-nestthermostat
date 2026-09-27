package sdm

import (
	"testing"

	smartdevicemanagement "google.golang.org/api/smartdevicemanagement/v1"
)

func relation(parent string, displayName string) *smartdevicemanagement.GoogleHomeEnterpriseSdmV1ParentRelation {
	return &smartdevicemanagement.GoogleHomeEnterpriseSdmV1ParentRelation{
		Parent:      parent,
		DisplayName: displayName,
	}
}

func TestFallbackName_StructureRoomAndLabel(t *testing.T) {
	relations := []*smartdevicemanagement.GoogleHomeEnterpriseSdmV1ParentRelation{
		relation("enterprises/E/structures/S", "Oasis"),
		relation("enterprises/E/structures/S/rooms/R", "Reception"),
	}

	got := fallbackName(relations, "Front Desk")

	if want := "Oasis Reception (Front Desk)"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFallbackName_RoomOnlyNoLabel(t *testing.T) {
	relations := []*smartdevicemanagement.GoogleHomeEnterpriseSdmV1ParentRelation{
		relation("enterprises/E/structures/S/rooms/R", "Reception"),
	}

	got := fallbackName(relations, "")

	if want := "Reception"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFallbackName_StructureOnlyNoRoom(t *testing.T) {
	relations := []*smartdevicemanagement.GoogleHomeEnterpriseSdmV1ParentRelation{
		relation("enterprises/E/structures/S", "Oasis"),
	}

	got := fallbackName(relations, "")

	if want := "Oasis"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFallbackName_NoRelationsFallsBackToLabel(t *testing.T) {
	got := fallbackName(nil, "Front Desk")

	if want := "Front Desk"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFallbackName_Empty(t *testing.T) {
	got := fallbackName(nil, "")

	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// TestFallbackName_UnrecognizedRelationShapeIsIgnored covers the real device that motivated this test:
// a single ParentRelation whose Parent path doesn't look like a room or a structure at all. It must not
// be mistaken for either — falling back to the label (or empty) is safer than surfacing a value that
// doesn't correspond to what the Nest app actually shows.
func TestFallbackName_UnrecognizedRelationShapeIsIgnored(t *testing.T) {
	relations := []*smartdevicemanagement.GoogleHomeEnterpriseSdmV1ParentRelation{
		relation("enterprises/E/devices/D", "Nest Thermostat"),
	}

	got := fallbackName(relations, "")

	if got != "" {
		t.Fatalf("expected unrecognized relation shape to be ignored, got %q", got)
	}
}
