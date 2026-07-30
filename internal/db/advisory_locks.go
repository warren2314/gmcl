package db

// AdminUserAdvisoryLockNamespace isolates locks that serialize security-sensitive
// admin account reads with role, deactivation, and deletion mutations.
const AdminUserAdvisoryLockNamespace int32 = 1196249932
