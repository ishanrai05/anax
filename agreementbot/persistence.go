package agreementbot

import (
    "encoding/json"
    "errors"
    "fmt"
    "github.com/boltdb/bolt"
    "github.com/golang/glog"
    "time"
)

const AGREEMENTS = "agreements"

type Agreement struct {
    CurrentAgreementId            string `json:"current_agreement_id"`     // unique
    AgreementProtocol             string `json:"agreement_protocol"`       // immutable after construction
    AgreementInceptionTime        uint64 `json:"agreement_inception_time"` // immutable after construction
    AgreementCreationTime         uint64 `json:"agreement_creation_time"`  // device responds affirmatively to proposal
    Proposal                      string `json:"proposal"`                 // JSON serialization of the proposal
    DataVerificationURL           string `json:"data_verification_URL"`    // The URL to use to ensure that this agreement is sending data. New in v1.6.2.
    DisableDataVerificationChecks bool   `json:"disable_data_verification_checks"` // disable data verification checks, assume data is being sent. New in v1.6.3
}

func (a Agreement) String() string {
    return fmt.Sprintf("CurrentAgreementId: %v , AgreementInceptionTime: %v, AgreementCreationTime: %v, Proposal: %v, DataVerificationURL: %v, DisableDataVerification: %v", a.CurrentAgreementId, a.AgreementInceptionTime, a.AgreementCreationTime, a.Proposal, a.DataVerificationURL, a.DisableDataVerificationChecks)
}

// private factory method for agreement w/out persistence safety:
func agreement(agreementid string, agreementProto string) (*Agreement, error) {
    if agreementid == "" || agreementProto == "" {
        return nil, errors.New("Illegal input: agreement id or agreement protocol is empty")
    } else {
        return &Agreement{
            CurrentAgreementId:            agreementid,
            AgreementProtocol:             agreementProto,
            AgreementInceptionTime:        uint64(time.Now().Unix()),
            AgreementCreationTime:         0,
            Proposal:                      "",
            DataVerificationURL:           "",
            DisableDataVerificationChecks: false,
        }, nil
    }
}

func AgreementAttempt(db *bolt.DB, agreementid string, agreementProto string) error {
    if agreement, err := agreement(agreementid, agreementProto); err != nil {
        return err
    } else if err := PersistNew(db, agreement.CurrentAgreementId, AGREEMENTS, &agreement); err != nil {
        return err
    } else {
        return nil
    }
}

func AgreementUpdate(db *bolt.DB, agreementid string, proposal string, url string, checks bool ) (*Agreement, error) {
    return AgreementMade(db, agreementid, proposal, url, checks)
}

func AgreementMade(db *bolt.DB, agreementid string, proposal string, url string, checks bool ) (*Agreement, error) {
    if agreement, err := singleAgreementUpdate(db, agreementid, func(a Agreement) *Agreement {
        a.AgreementCreationTime = uint64(time.Now().Unix())
        a.Proposal = proposal
        a.DataVerificationURL = url
        a.DisableDataVerificationChecks = checks
        return &a
    }); err != nil {
        return nil, err
    } else {
        return agreement, nil
    }
}

// no error on not found, only nil
func FindSingleAgreementByAgreementId(db *bolt.DB, agreementid string) (*Agreement, error) {
    filters := make([]AFilter, 0)
    filters = append(filters, IdAFilter(agreementid))

    if agreements, err := FindAgreements(db, filters); err != nil {
        return nil, err
    } else if len(agreements) > 1 {
        return nil, fmt.Errorf("Expected only one record for agreementid: %v, but retrieved: %v", agreementid, agreements)
    } else if len(agreements) == 0 {
        return nil, nil
    } else {
        return &agreements[0], nil
    }
}

func singleAgreementUpdate(db *bolt.DB, agreementid string, fn func(Agreement) *Agreement) (*Agreement, error) {
    if agreement, err := FindSingleAgreementByAgreementId(db, agreementid); err != nil {
        return nil, err
    } else if agreement == nil {
        return nil, fmt.Errorf("Unable to locate agreement id: %v", agreementid)
    } else {
        updated := fn(*agreement)
        return updated, persistUpdatedAgreement(db, agreementid, updated)
    }
}

// does whole-member replacements of values that are legal to change during the course of an agreement's life
func persistUpdatedAgreement(db *bolt.DB, agreementid string, update *Agreement) error {
    return db.Update(func(tx *bolt.Tx) error {
        if b, err := tx.CreateBucketIfNotExists([]byte(AGREEMENTS)); err != nil {
            return err
        } else {
            current := b.Get([]byte(agreementid))
            var mod Agreement

            if current == nil {
                return fmt.Errorf("No agreement with given id available to update: %v", agreementid)
            } else if err := json.Unmarshal(current, &mod); err != nil {
                return fmt.Errorf("Failed to unmarshal agreement DB data: %v", string(current))
            } else {

                // write updates only to the fields we expect should be updateable
                mod.AgreementCreationTime = update.AgreementCreationTime
                mod.Proposal = update.Proposal
                mod.DataVerificationURL = update.DataVerificationURL
                mod.DisableDataVerificationChecks = update.DisableDataVerificationChecks

                if serialized, err := json.Marshal(mod); err != nil {
                    return fmt.Errorf("Failed to serialize agreement record: %v", mod)
                } else if err := b.Put([]byte(agreementid), serialized); err != nil {
                    return fmt.Errorf("Failed to write record with key: %v", agreementid)
                } else {
                    glog.V(2).Infof("Succeeded updating agreement record to %v", mod)
                }
            }
        }
        return nil
    })
}

func DeleteAgreement(db *bolt.DB, pk string) error {
    if pk == "" {
        return fmt.Errorf("Missing required arg pk")
    } else {

        return db.Update(func(tx *bolt.Tx) error {
            b := tx.Bucket([]byte(AGREEMENTS))
            if b == nil {
                return fmt.Errorf("Unknown bucket: %v", AGREEMENTS)
            } else if existing := b.Get([]byte(pk)); existing == nil {
                glog.Errorf("Warning: record deletion requested, but record does not exist: %v", pk)
                return nil // handle already-deleted agreement as success
            } else {
                var record Agreement

                if err := json.Unmarshal(existing, &record); err != nil {
                    glog.Errorf("Error deserializing agreement: %v. This is a pre-deletion warning message function so deletion will still proceed", record)
                } else if record.CurrentAgreementId != "" {
                    glog.Errorf("Warning! Deleting an agreement record with an agreement id, this operation should only be done after cancelling on the blockchain.")
                }
            }

            return b.Delete([]byte(pk))
        })
    }
}

func IdAFilter(id string) AFilter {
    return func(a Agreement) bool { return a.CurrentAgreementId == id }
}

type AFilter func(Agreement) bool

func FindAgreements(db *bolt.DB, filters []AFilter) ([]Agreement, error) {
    agreements := make([]Agreement, 0)

    readErr := db.View(func(tx *bolt.Tx) error {

        if b := tx.Bucket([]byte(AGREEMENTS)); b != nil {
            b.ForEach(func(k, v []byte) error {

                var a Agreement

                if err := json.Unmarshal(v, &a); err != nil {
                    glog.Errorf("Unable to deserialize db record: %v", v)
                } else {
                    glog.V(5).Infof("Demarshalled agreement in DB: %v", a)
                    exclude := false
                    for _, filterFn := range filters {
                        if !filterFn(a) {
                            exclude = true
                        }
                    }
                    if !exclude {
                        agreements = append(agreements, a)
                    }
                }
                return nil
            })
        }

        return nil // end the transaction
    })

    if readErr != nil {
        return nil, readErr
    } else {
        return agreements, nil
    }
}

func PersistNew(db *bolt.DB, pk string, bucket string, record interface{}) error {
    if pk == "" || bucket == "" {
        return fmt.Errorf("Missing required args, pk and/or bucket")
    } else {
        writeErr := db.Update(func(tx *bolt.Tx) error {

            if b, err := tx.CreateBucketIfNotExists([]byte(bucket)); err != nil {
                return err
            } else if existing := b.Get([]byte(pk)); existing != nil {
                return fmt.Errorf("Bucket %v already contains record with primary key: %v", bucket, pk)
            } else if bytes, err := json.Marshal(record); err != nil {
                return fmt.Errorf("Unable to serialize record %v. Error: %v", record, err)
            } else if err := b.Put([]byte(pk), bytes); err != nil {
                return fmt.Errorf("Unable to write to record to bucket %v. Primary key of record: %v", bucket, pk)
            } else {
                glog.V(2).Infof("Succeeded writing record identified by %v in %v", pk, bucket)
                return nil
            }
        })

        return writeErr
    }
}
