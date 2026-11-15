package main

// currentInstallControlPrefix is the control prefix fixture applications commit.
// Management fixtures that address the canonical API directly through
// canonicalAPI (not through the mounted route) name that same prefix instead of
// an unrelated fixed literal, so a fixture can never pass by depending on a
// control path its installation did not commit.
const currentInstallControlPrefix = "/cpamp-admin"

// currentInstallManagementAPIV2Prefix is the canonical Public API base for that
// committed control prefix.
const currentInstallManagementAPIV2Prefix = currentInstallControlPrefix + "/api/v2/"
