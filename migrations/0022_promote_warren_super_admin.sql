UPDATE admin_users
SET role = 'super_admin'
WHERE LOWER(username) IN ('admin', 'warren', 'warre', 'warren2314')
   OR LOWER(email) IN ('webmaster@gmcl.co.uk', 'warren@gmcl.co.uk')
   OR LOWER(email) LIKE 'warren%@%';
