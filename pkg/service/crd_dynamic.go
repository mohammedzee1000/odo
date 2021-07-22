package service

import "fmt"

// DynamicCRD holds the original CR obtained from the Operator (a CSV), or user
// (when they use --from-file flag), and few other attributes that are likely
// to be used to validate a CRD before creating a service from it
type DynamicCRD struct {
	// contains the CR as obtained from CSV or user
	OriginalCRD map[string]interface{}
}

func NewDynamicCRD() *DynamicCRD {
	return &DynamicCRD{}
}

// ValidateMetadataInCRD validates if the CRD has metadata.name field and returns an error
func (d *DynamicCRD) ValidateMetadataInCRD() error {
	metadata, ok := d.OriginalCRD["metadata"].(map[string]interface{})
	if !ok {
		// this condition is satisfied if there's no metadata at all in the provided CRD
		return fmt.Errorf("couldn't find \"metadata\" in the yaml; need metadata start the service")
	}

	if _, ok := metadata["name"].(string); ok {
		// found the metadata.name; no error
		return nil
	}
	return fmt.Errorf("couldn't find metadata.name in the yaml; provide a name for the service")
}

// SetServiceName modifies the CRD to contain user provided name on the CLI
// instead of using the default one in almExample
func (d *DynamicCRD) SetServiceName(name string) {
	metaMap := d.OriginalCRD["metadata"].(map[string]interface{})

	for k := range metaMap {
		if k == "name" {
			metaMap[k] = name
			return
		}
	}
	metaMap["name"] = name
}

// GetServiceNameFromCRD fetches the service name from metadata.name field of the CRD
func (d *DynamicCRD) GetServiceNameFromCRD() (string, error) {
	metadata, ok := d.OriginalCRD["metadata"].(map[string]interface{})
	if !ok {
		// this condition is satisfied if there's no metadata at all in the provided CRD
		return "", fmt.Errorf("couldn't find \"metadata\" in the yaml; need metadata.name to start the service")
	}

	if name, ok := metadata["name"].(string); ok {
		// found the metadata.name; no error
		return name, nil
	}
	return "", fmt.Errorf("couldn't find metadata.name in the yaml; provide a name for the service")
}

// AddComponentLabelsToCRD appends odo labels to CRD if "labels" field already exists in metadata; else creates labels
func (d *DynamicCRD) AddComponentLabelsToCRD(labels map[string]string) {
	metaMap := d.OriginalCRD["metadata"].(map[string]interface{})

	for k := range metaMap {
		if k == "labels" {
			metaLabels := metaMap["labels"].(map[string]interface{})
			for i := range labels {
				metaLabels[i] = labels[i]
			}
			return
		}
	}
	// if metadata doesn't have 'labels' field, we set it up
	metaMap["labels"] = labels
}
