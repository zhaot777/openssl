// Copyright (C) 2017. See AUTHORS.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package openssl

// #include "openssl/engine.h"
// #include "shim.h"
import "C"

import (
	"errors"
	"io/ioutil"
	"runtime"
	"unsafe"
)

var ( // some (effectively) constants for tests to refer to
	ed25519_support = C.X_ED25519_SUPPORT != 0
)

type Method *C.EVP_MD

var (
	SHA1_Method   Method = C.X_EVP_sha1()
	SHA256_Method Method = C.X_EVP_sha256()
	SHA512_Method Method = C.X_EVP_sha512()
)

// Constants for the various key types.
// Mapping of name -> NID taken from openssl/evp.h
const (
	KeyTypeNone    = NID_undef
	KeyTypeRSA     = NID_rsaEncryption
	KeyTypeRSA2    = NID_rsa
	KeyTypeDSA     = NID_dsa
	KeyTypeDSA1    = NID_dsa_2
	KeyTypeDSA2    = NID_dsaWithSHA
	KeyTypeDSA3    = NID_dsaWithSHA1
	KeyTypeDSA4    = NID_dsaWithSHA1_2
	KeyTypeDH      = NID_dhKeyAgreement
	KeyTypeDHX     = NID_dhpublicnumber
	KeyTypeEC      = NID_X9_62_id_ecPublicKey
	KeyTypeHMAC    = NID_hmac
	KeyTypeCMAC    = NID_cmac
	KeyTypeTLS1PRF = NID_tls1_prf
	KeyTypeHKDF    = NID_hkdf
	KeyTypeX25519  = NID_X25519
	KeyTypeX448    = NID_X448
	KeyTypeED25519 = NID_ED25519
	KeyTypeED448   = NID_ED448
)

type PublicKey interface {
	// Verifies the data signature using PKCS1.15
	VerifyPKCS1v15(method Method, data, sig []byte) error

	// MarshalPKIXPublicKeyPEM converts the public key to PEM-encoded PKIX
	// format
	MarshalPKIXPublicKeyPEM() (pem_block []byte, err error)

	// MarshalPKIXPublicKeyDER converts the public key to DER-encoded PKIX
	// format
	MarshalPKIXPublicKeyDER() (der_block []byte, err error)

	// KeyType returns an identifier for what kind of key is represented by this
	// object.
	KeyType() NID

	// BaseType returns an identifier for what kind of key is represented
	// by this object.
	// Keys that share same algorithm but use different legacy formats
	// will have the same BaseType.
	//
	// For example, a key with a `KeyType() == KeyTypeRSA` and a key with a
	// `KeyType() == KeyTypeRSA2` would both have `BaseType() == KeyTypeRSA`.
	BaseType() NID

	evpPKey() *C.EVP_PKEY
}

type PrivateKey interface {
	PublicKey

	// Signs the data using PKCS1.15
	SignPKCS1v15(Method, []byte) ([]byte, error)

	// MarshalPKCS1PrivateKeyPEM converts the private key to PEM-encoded PKCS1
	// format
	MarshalPKCS1PrivateKeyPEM() (pem_block []byte, err error)

	// MarshalPKCS1PrivateKeyDER converts the private key to DER-encoded PKCS1
	// format
	MarshalPKCS1PrivateKeyDER() (der_block []byte, err error)
}

type pKey struct {
	key        *C.EVP_PKEY
	engine_ref interface{} //see comment below in EngineLoadPrivateKey
}

func (key *pKey) evpPKey() *C.EVP_PKEY { return key.key }

func (key *pKey) KeyType() NID {
	nid := C.EVP_PKEY_id(key.key)
	runtime.KeepAlive(key)
	return NID(nid)
}

func (key *pKey) BaseType() NID {
	nid := C.EVP_PKEY_base_id(key.key)
	runtime.KeepAlive(key)
	return NID(nid)
}

func (key *pKey) SignPKCS1v15(method Method, data []byte) ([]byte, error) {

	ctx := C.X_EVP_MD_CTX_new()
	defer C.X_EVP_MD_CTX_free(ctx)

	if key.KeyType() == KeyTypeED25519 {
		// do ED specific one-shot sign

		if method != nil || len(data) == 0 {
			return nil, errors.New("signpkcs1v15: 0-length data or non-null digest")
		}

		if 1 != C.X_EVP_DigestSignInit(ctx, nil, nil, nil, key.key) {
			return nil, errors.New("signpkcs1v15: failed to init signature")
		}

		// evp signatures are 64 bytes
		sig := make([]byte, 64, 64)
		var sigblen C.size_t = 64
		if 1 != C.X_EVP_DigestSign(ctx,
			((*C.uchar)(unsafe.Pointer(&sig[0]))),
			&sigblen,
			(*C.uchar)(unsafe.Pointer(&data[0])),
			C.size_t(len(data))) {
			return nil, errors.New("signpkcs1v15: failed to do one-shot signature")
		}
		runtime.KeepAlive(key)
		runtime.KeepAlive(data)
		return sig[:sigblen], nil
	} else {
		if 1 != C.X_EVP_SignInit(ctx, method) {
			return nil, errors.New("signpkcs1v15: failed to init signature")
		}
		if len(data) > 0 {
			if 1 != C.X_EVP_SignUpdate(
				ctx, unsafe.Pointer(&data[0]), C.uint(len(data))) {
				return nil, errors.New("signpkcs1v15: failed to update signature")
			}
			runtime.KeepAlive(data)
		}
		sig := make([]byte, C.X_EVP_PKEY_size(key.key))
		var sigblen C.uint
		if 1 != C.X_EVP_SignFinal(ctx,
			((*C.uchar)(unsafe.Pointer(&sig[0]))), &sigblen, key.key) {
			return nil, errors.New("signpkcs1v15: failed to finalize signature")
		}
		runtime.KeepAlive(key)
		return sig[:sigblen], nil
	}
}

func (key *pKey) VerifyPKCS1v15(method Method, data, sig []byte) error {
	ctx := C.X_EVP_MD_CTX_new()
	defer C.X_EVP_MD_CTX_free(ctx)

	if key.KeyType() == KeyTypeED25519 {
		// do ED specific one-shot sign

		if method != nil || len(data) == 0 || len(sig) == 0 {
			return errors.New("verifypkcs1v15: 0-length data or sig or non-null digest")
		}

		if 1 != C.X_EVP_DigestVerifyInit(ctx, nil, nil, nil, key.key) {
			return errors.New("verifypkcs1v15: failed to init verify")
		}

		if 1 != C.X_EVP_DigestVerify(ctx,
			((*C.uchar)(unsafe.Pointer(&sig[0]))),
			C.size_t(len(sig)),
			(*C.uchar)(unsafe.Pointer(&data[0])),
			C.size_t(len(data))) {
			return errors.New("verifypkcs1v15: failed to do one-shot verify")
		}
		runtime.KeepAlive(data)
		runtime.KeepAlive(sig)
		runtime.KeepAlive(key)
		return nil

	} else {
		if 1 != C.X_EVP_VerifyInit(ctx, method) {
			return errors.New("verifypkcs1v15: failed to init verify")
		}
		if len(data) > 0 {
			if 1 != C.X_EVP_VerifyUpdate(
				ctx, unsafe.Pointer(&data[0]), C.uint(len(data))) {
				return errors.New("verifypkcs1v15: failed to update verify")
			}
		}
		if 1 != C.X_EVP_VerifyFinal(ctx,
			((*C.uchar)(unsafe.Pointer(&sig[0]))), C.uint(len(sig)), key.key) {
			return errors.New("verifypkcs1v15: failed to finalize verify")
		}
		runtime.KeepAlive(data)
		runtime.KeepAlive(sig)
		runtime.KeepAlive(key)
		return nil
	}
}

func (key *pKey) MarshalPKCS1PrivateKeyPEM() (pem_block []byte,
	err error) {
	bio := C.BIO_new(C.BIO_s_mem())
	if bio == nil {
		return nil, errors.New("failed to allocate memory BIO")
	}
	defer C.BIO_free(bio)

	// PEM_write_bio_PrivateKey_traditional will use the key-specific PKCS1
	// format if one is available for that key type, otherwise it will encode
	// to a PKCS8 key.
	if int(C.X_PEM_write_bio_PrivateKey_traditional(bio, key.key, nil, nil,
		C.int(0), nil, nil)) != 1 {
		return nil, errors.New("failed dumping private key")
	}
	runtime.KeepAlive(key)

	return ioutil.ReadAll(asAnyBio(bio))
}

func (key *pKey) MarshalPKCS1PrivateKeyDER() (der_block []byte,
	err error) {
	bio := C.BIO_new(C.BIO_s_mem())
	if bio == nil {
		return nil, errors.New("failed to allocate memory BIO")
	}
	defer C.BIO_free(bio)

	if int(C.i2d_PrivateKey_bio(bio, key.key)) != 1 {
		return nil, errors.New("failed dumping private key der")
	}
	runtime.KeepAlive(key)

	return ioutil.ReadAll(asAnyBio(bio))
}

func (key *pKey) MarshalPKIXPublicKeyPEM() (pem_block []byte,
	err error) {
	bio := C.BIO_new(C.BIO_s_mem())
	if bio == nil {
		return nil, errors.New("failed to allocate memory BIO")
	}
	defer C.BIO_free(bio)

	if int(C.PEM_write_bio_PUBKEY(bio, key.key)) != 1 {
		return nil, errors.New("failed dumping public key pem")
	}
	runtime.KeepAlive(key)

	return ioutil.ReadAll(asAnyBio(bio))
}

func (key *pKey) MarshalPKIXPublicKeyDER() (der_block []byte,
	err error) {
	bio := C.BIO_new(C.BIO_s_mem())
	if bio == nil {
		return nil, errors.New("failed to allocate memory BIO")
	}
	defer C.BIO_free(bio)

	if int(C.i2d_PUBKEY_bio(bio, key.key)) != 1 {
		return nil, errors.New("failed dumping public key der")
	}
	runtime.KeepAlive(key)

	return ioutil.ReadAll(asAnyBio(bio))
}

// EngineLoadPrivateKey loads a private key by id
// the id is a pkcs#11 URI https://tools.ietf.org/html/rfc7512#section-2.3
// Engine comes from e.g.: e,err:=openssl.EngineById("pkcs11")
func EngineLoadPrivateKey(e *Engine, id string) (PrivateKey, error) {
	if e == nil {
		return nil, errors.New("ENGINE_load_private_key cannot be called with NULL engine")
	}

	keyID := C.CString(id)
	defer C.free(unsafe.Pointer(keyID))

	key := C.ENGINE_load_private_key(e.e, keyID, nil, nil)
	if key == nil {
		return nil, errors.New("cannot load private key, ENGINE_load_private_key error")
	}

	// engine_ref trick inspired by the work of Renato Aguiar https://github.com/renatoaguiar
	// it prevents the engine to be freed while we still use the key.
	p := &pKey{key: key, engine_ref: e}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// LoadPrivateKeyFromPEM loads a private key from a PEM-encoded block.
func LoadPrivateKeyFromPEM(pem_block []byte) (PrivateKey, error) {
	if len(pem_block) == 0 {
		return nil, errors.New("empty pem block")
	}
	bio := C.BIO_new_mem_buf(unsafe.Pointer(&pem_block[0]),
		C.int(len(pem_block)))
	runtime.KeepAlive(pem_block)
	if bio == nil {
		return nil, errors.New("failed creating bio")
	}
	defer C.BIO_free(bio)

	key := C.PEM_read_bio_PrivateKey(bio, nil, nil, nil)
	if key == nil {
		return nil, errors.New("failed reading private key")
	}

	p := &pKey{key: key}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// LoadPrivateKeyFromPEMWithPassword loads a private key from a PEM-encoded block.
func LoadPrivateKeyFromPEMWithPassword(pem_block []byte, password string) (
	PrivateKey, error) {
	if len(pem_block) == 0 {
		return nil, errors.New("empty pem block")
	}
	bio := C.BIO_new_mem_buf(unsafe.Pointer(&pem_block[0]),
		C.int(len(pem_block)))
	runtime.KeepAlive(pem_block)
	if bio == nil {
		return nil, errors.New("failed creating bio")
	}
	defer C.BIO_free(bio)
	cs := C.CString(password)
	defer C.free(unsafe.Pointer(cs))
	key := C.PEM_read_bio_PrivateKey(bio, nil, nil, unsafe.Pointer(cs))
	if key == nil {
		return nil, errors.New("failed reading private key")
	}

	p := &pKey{key: key}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// LoadPrivateKeyFromDER loads a private key from a DER-encoded block.
func LoadPrivateKeyFromDER(der_block []byte) (PrivateKey, error) {
	if len(der_block) == 0 {
		return nil, errors.New("empty der block")
	}
	bio := C.BIO_new_mem_buf(unsafe.Pointer(&der_block[0]),
		C.int(len(der_block)))
	runtime.KeepAlive(der_block)
	if bio == nil {
		return nil, errors.New("failed creating bio")
	}
	defer C.BIO_free(bio)

	key := C.d2i_PrivateKey_bio(bio, nil)
	if key == nil {
		return nil, errors.New("failed reading private key der")
	}

	p := &pKey{key: key}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// LoadPrivateKeyFromPEMWidthPassword loads a private key from a PEM-encoded block.
// Backwards-compatible with typo
func LoadPrivateKeyFromPEMWidthPassword(pem_block []byte, password string) (
	PrivateKey, error) {
	return LoadPrivateKeyFromPEMWithPassword(pem_block, password)
}

// LoadPublicKeyFromPEM loads a public key from a PEM-encoded block.
func LoadPublicKeyFromPEM(pem_block []byte) (PublicKey, error) {
	if len(pem_block) == 0 {
		return nil, errors.New("empty pem block")
	}
	bio := C.BIO_new_mem_buf(unsafe.Pointer(&pem_block[0]),
		C.int(len(pem_block)))
	runtime.KeepAlive(pem_block)
	if bio == nil {
		return nil, errors.New("failed creating bio")
	}
	defer C.BIO_free(bio)

	key := C.PEM_read_bio_PUBKEY(bio, nil, nil, nil)
	if key == nil {
		return nil, errors.New("failed reading public key der")
	}

	p := &pKey{key: key}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// LoadPublicKeyFromDER loads a public key from a DER-encoded block.
func LoadPublicKeyFromDER(der_block []byte) (PublicKey, error) {
	if len(der_block) == 0 {
		return nil, errors.New("empty der block")
	}
	bio := C.BIO_new_mem_buf(unsafe.Pointer(&der_block[0]),
		C.int(len(der_block)))
	runtime.KeepAlive(der_block)
	if bio == nil {
		return nil, errors.New("failed creating bio")
	}
	defer C.BIO_free(bio)

	key := C.d2i_PUBKEY_bio(bio, nil)
	if key == nil {
		return nil, errors.New("failed reading public key der")
	}

	p := &pKey{key: key}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// GenerateRSAKey generates a new RSA private key with an exponent of 65537.
func GenerateRSAKey(bits int) (PrivateKey, error) {
	return GenerateRSAKeyWithExponent(bits, 65537)
}

// GenerateRSAKeyWithExponent generates a new RSA private key.
func GenerateRSAKeyWithExponent(bits int, exponent int) (PrivateKey, error) {
	exp := C.BN_new()
	if exp == nil {
		return nil, errors.New("failed to allocate BIGNUM for the exponent")
	}
	defer C.BN_free(exp)
	rsa := C.RSA_new()
	if rsa == nil {
		return nil, errors.New("failed to allocate RSA key")
	}
	ret := C.BN_set_word(exp, C.ulong(uint(exponent)))
	if ret == 0 {
		C.RSA_free(rsa)
		return nil, errors.New("error assigning exponent to BIGNUM")
	}
	ret = C.RSA_generate_key_ex(rsa, C.int(bits), exp, nil)
	if ret == 0 {
		C.RSA_free(rsa)
		return nil, errors.New("failed to generate RSA key")
	}
	key := C.X_EVP_PKEY_new()
	if key == nil {
		C.RSA_free(rsa)
		return nil, errors.New("failed to allocate EVP_PKEY")
	}
	if C.X_EVP_PKEY_assign_charp(key, C.EVP_PKEY_RSA, (*C.char)(unsafe.Pointer(rsa))) != 1 {
		C.RSA_free(rsa)
		return nil, errors.New("failed to assign RSA key")
	}
	p := &pKey{key: key}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// GenerateECKey generates a new elliptic curve private key on the speicified
// curve.
func GenerateECKey(curve EllipticCurve) (PrivateKey, error) {

	// Create context for parameter generation
	paramCtx := C.EVP_PKEY_CTX_new_id(C.EVP_PKEY_EC, nil)
	if paramCtx == nil {
		return nil, errors.New("failed creating EC parameter generation context")
	}
	defer C.EVP_PKEY_CTX_free(paramCtx)

	// Intialize the parameter generation
	if int(C.EVP_PKEY_paramgen_init(paramCtx)) != 1 {
		return nil, errors.New("failed initializing EC parameter generation context")
	}

	// Set curve in EC parameter generation context
	if int(C.X_EVP_PKEY_CTX_set_ec_paramgen_curve_nid(paramCtx, C.int(curve))) != 1 {
		return nil, errors.New("failed setting curve in EC parameter generation context")
	}

	// Create parameter object
	var params *C.EVP_PKEY
	if int(C.EVP_PKEY_paramgen(paramCtx, &params)) != 1 {
		return nil, errors.New("failed creating EC key generation parameters")
	}
	defer C.EVP_PKEY_free(params)

	// Create context for the key generation
	keyCtx := C.EVP_PKEY_CTX_new(params, nil)
	if keyCtx == nil {
		return nil, errors.New("failed creating EC key generation context")
	}
	defer C.EVP_PKEY_CTX_free(keyCtx)

	// Generate the key
	var privKey *C.EVP_PKEY
	if int(C.EVP_PKEY_keygen_init(keyCtx)) != 1 {
		return nil, errors.New("failed initializing EC key generation context")
	}
	if int(C.EVP_PKEY_keygen(keyCtx, &privKey)) != 1 {
		return nil, errors.New("failed generating EC private key")
	}

	p := &pKey{key: privKey}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}

// GenerateED25519Key generates a Ed25519 key
func GenerateED25519Key() (PrivateKey, error) {
	// Key context
	keyCtx := C.EVP_PKEY_CTX_new_id(C.X_EVP_PKEY_ED25519, nil)
	if keyCtx == nil {
		return nil, errors.New("failed creating EC parameter generation context")
	}
	defer C.EVP_PKEY_CTX_free(keyCtx)

	// Generate the key
	var privKey *C.EVP_PKEY
	if int(C.EVP_PKEY_keygen_init(keyCtx)) != 1 {
		return nil, errors.New("failed initializing ED25519 key generation context")
	}
	if int(C.EVP_PKEY_keygen(keyCtx, &privKey)) != 1 {
		return nil, errors.New("failed generating ED25519 private key")
	}

	p := &pKey{key: privKey}
	runtime.SetFinalizer(p, func(p *pKey) {
		C.X_EVP_PKEY_free(p.key)
	})
	return p, nil
}
