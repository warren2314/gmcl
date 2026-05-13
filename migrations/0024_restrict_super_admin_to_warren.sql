UPDATE admin_users
SET role = 'admin'
WHERE LOWER(username) <> 'warren2314';

UPDATE admin_users
SET role = 'super_admin'
WHERE LOWER(username) = 'warren2314';
