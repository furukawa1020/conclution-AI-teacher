//! Versioned client-side envelope encryption for stored recordings.
//!
//! A nonce must never be reused with the same [`RecordingKey`]. Nonce creation
//! belongs to the platform layer so Web Crypto and Android SecureRandom can be
//! used directly.

use aes_gcm::{
    Aes256Gcm, Nonce,
    aead::{Aead, KeyInit, Payload},
};
use thiserror::Error;
use zeroize::{Zeroize, Zeroizing};

pub const FORMAT_VERSION: u8 = 1;
pub const KEY_BYTES: usize = 32;
pub const NONCE_BYTES: usize = 12;
const AAD_DOMAIN: &[u8] = b"kotae.audio.v1";

pub struct RecordingKey(Zeroizing<[u8; KEY_BYTES]>);

impl RecordingKey {
    pub fn from_bytes(bytes: [u8; KEY_BYTES]) -> Self {
        Self(Zeroizing::new(bytes))
    }

    fn expose(&self) -> &[u8; KEY_BYTES] {
        &self.0
    }
}

impl core::fmt::Debug for RecordingKey {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        formatter.write_str("RecordingKey([REDACTED])")
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[repr(u8)]
pub enum AudioCodec {
    OpusWebM = 1,
    OpusOgg = 2,
    Pcm16Le = 3,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct RecordingBinding {
    pub user_binding: [u8; 32],
    pub recording_id: [u8; 16],
    pub codec: AudioCodec,
    pub sample_rate_hz: u32,
    pub channels: u8,
}

#[derive(Debug, PartialEq, Eq)]
pub struct EncryptedRecording {
    pub format_version: u8,
    pub nonce: [u8; NONCE_BYTES],
    pub ciphertext: Vec<u8>,
}

#[derive(Debug, Error, PartialEq, Eq)]
pub enum VaultError {
    #[error("recording metadata is invalid")]
    InvalidMetadata,
    #[error("audio encryption failed")]
    EncryptionFailed,
    #[error("audio authentication or decryption failed")]
    AuthenticationFailed,
}

pub fn seal(
    key: &RecordingKey,
    nonce: [u8; NONCE_BYTES],
    plaintext: &[u8],
    binding: &RecordingBinding,
) -> Result<EncryptedRecording, VaultError> {
    validate_binding(binding)?;
    let cipher =
        Aes256Gcm::new_from_slice(key.expose()).map_err(|_| VaultError::EncryptionFailed)?;
    let aad = encode_aad(binding);
    let ciphertext = cipher
        .encrypt(
            Nonce::from_slice(&nonce),
            Payload {
                msg: plaintext,
                aad: &aad,
            },
        )
        .map_err(|_| VaultError::EncryptionFailed)?;

    Ok(EncryptedRecording {
        format_version: FORMAT_VERSION,
        nonce,
        ciphertext,
    })
}

pub fn open(
    key: &RecordingKey,
    encrypted: &EncryptedRecording,
    binding: &RecordingBinding,
) -> Result<Zeroizing<Vec<u8>>, VaultError> {
    validate_binding(binding)?;
    if encrypted.format_version != FORMAT_VERSION {
        return Err(VaultError::AuthenticationFailed);
    }

    let cipher =
        Aes256Gcm::new_from_slice(key.expose()).map_err(|_| VaultError::AuthenticationFailed)?;
    let aad = encode_aad(binding);
    let mut plaintext = cipher
        .decrypt(
            Nonce::from_slice(&encrypted.nonce),
            Payload {
                msg: &encrypted.ciphertext,
                aad: &aad,
            },
        )
        .map_err(|_| VaultError::AuthenticationFailed)?;

    let protected = Zeroizing::new(core::mem::take(&mut plaintext));
    plaintext.zeroize();
    Ok(protected)
}

fn validate_binding(binding: &RecordingBinding) -> Result<(), VaultError> {
    if binding.sample_rate_hz < 8_000
        || binding.sample_rate_hz > 192_000
        || !(1..=2).contains(&binding.channels)
        || binding.recording_id == [0; 16]
        || binding.user_binding == [0; 32]
    {
        return Err(VaultError::InvalidMetadata);
    }
    Ok(())
}

fn encode_aad(binding: &RecordingBinding) -> [u8; 69] {
    let mut encoded = [0_u8; 69];
    encoded[..14].copy_from_slice(AAD_DOMAIN);
    encoded[14] = FORMAT_VERSION;
    encoded[15..47].copy_from_slice(&binding.user_binding);
    encoded[47..63].copy_from_slice(&binding.recording_id);
    encoded[63] = binding.codec as u8;
    encoded[64..68].copy_from_slice(&binding.sample_rate_hz.to_be_bytes());
    encoded[68] = binding.channels;
    encoded
}

#[cfg(test)]
mod tests {
    use super::*;

    fn binding() -> RecordingBinding {
        RecordingBinding {
            user_binding: [7; 32],
            recording_id: [9; 16],
            codec: AudioCodec::OpusWebM,
            sample_rate_hz: 48_000,
            channels: 1,
        }
    }

    #[test]
    fn round_trip_and_redacted_debug_key() {
        let key = RecordingKey::from_bytes([3; KEY_BYTES]);
        let encrypted = seal(&key, [4; NONCE_BYTES], b"private voice", &binding())
            .expect("encrypt recording");
        let plaintext = open(&key, &encrypted, &binding()).expect("decrypt recording");

        assert_eq!(&*plaintext, b"private voice");
        assert_eq!(format!("{key:?}"), "RecordingKey([REDACTED])");
    }

    #[test]
    fn metadata_is_cryptographically_bound() {
        let key = RecordingKey::from_bytes([3; KEY_BYTES]);
        let encrypted =
            seal(&key, [4; NONCE_BYTES], b"private voice", &binding()).expect("encrypt recording");
        let mut changed = binding();
        changed.sample_rate_hz = 44_100;

        assert_eq!(
            open(&key, &encrypted, &changed),
            Err(VaultError::AuthenticationFailed)
        );
    }

    #[test]
    fn rejects_zero_identifiers() {
        let key = RecordingKey::from_bytes([3; KEY_BYTES]);
        let mut invalid = binding();
        invalid.recording_id = [0; 16];

        assert_eq!(
            seal(&key, [4; NONCE_BYTES], b"voice", &invalid),
            Err(VaultError::InvalidMetadata)
        );
    }
}
