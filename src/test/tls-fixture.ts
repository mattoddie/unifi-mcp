/**
 * A throwaway self-signed certificate and key for the HTTPS mock controller
 * (CN "unifi-mcp test controller", SAN IP:127.0.0.1, valid until 2126). They
 * were generated for these tests only, protect nothing and are not secret.
 */
export const TEST_TLS_CERT = `
-----BEGIN CERTIFICATE-----
MIIBrzCCAVagAwIBAgIUfcsBdZKORvv2vBxullosgbQnAsswCgYIKoZIzj0EAwIw
JDEiMCAGA1UEAwwZdW5pZmktbWNwIHRlc3QgY29udHJvbGxlcjAgFw0yNjEwMTUy
MjQ3MjVaGA8yMTI2MDkyMTIyNDcyNVowJDEiMCAGA1UEAwwZdW5pZmktbWNwIHRl
c3QgY29udHJvbGxlcjBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABFcVt9AbzX0j
GgFn61jQNkuPP41ArBlYyBDOPYIqhtUI7v2mEydt9Rdq+i9JMA553qw2KH1sTbaJ
WvFasFfA7DejZDBiMB0GA1UdDgQWBBSPjmiB0XLnFAlIBNBWH8Wcg9P01TAfBgNV
HSMEGDAWgBSPjmiB0XLnFAlIBNBWH8Wcg9P01TAPBgNVHREECDAGhwR/AAABMA8G
A1UdEwEB/wQFMAMBAf8wCgYIKoZIzj0EAwIDRwAwRAIgHdaQWorUnUTwmrxiJhjs
5kIfOCCoeTlaphUCFh6w9ZsCIHYtlkPOeT72V6ATq1PWzgRwaDEIdv7zc+UZ2f7n
267b
-----END CERTIFICATE-----
`;

export const TEST_TLS_KEY = `
-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgVXtUjbHGiy2ZhpkT
gdbu0kICLXsoSay+Z514pV6tL6ehRANCAARXFbfQG819IxoBZ+tY0DZLjz+NQKwZ
WMgQzj2CKobVCO79phMnbfUXavovSTAOed6sNih9bE22iVrxWrBXwOw3
-----END PRIVATE KEY-----
`;
