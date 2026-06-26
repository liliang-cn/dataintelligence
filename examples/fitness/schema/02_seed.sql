-- Deterministic seed (setseed → stable numbers for eval). Realistic boutique
-- reformer-pilates data: 6 studios, a Solidcore-style class menu, 18 instructors,
-- 600 members, ~60 days of sessions, bookings with real no-show/fill patterns,
-- and membership/class-pack payments.
SELECT setseed(0.42);

INSERT INTO studios (studio_id, name, city, region, reformers, opened_at) VALUES
 (1,'14th Street','Washington DC','Mid-Atlantic',14,'2019-03-01'),
 (2,'Georgetown','Washington DC','Mid-Atlantic',12,'2021-06-15'),
 (3,'Flatiron','New York','Northeast',16,'2018-09-10'),
 (4,'Back Bay','Boston','Northeast',12,'2020-01-20'),
 (5,'West Loop','Chicago','Midwest',14,'2021-11-05'),
 (6,'Silver Lake','Los Angeles','West',16,'2019-07-22');

INSERT INTO class_types (class_type_id, name, intensity, duration_min) VALUES
 (1,'Full Body','signature',50),
 (2,'Advanced Full Body','advanced',50),
 (3,'Arms + Abs','signature',50),
 (4,'Lower Body','signature',50),
 (5,'Total Core','signature',50),
 (6,'Foundations','beginner',50);

INSERT INTO instructors (instructor_id, name, studio_id, hired_at)
SELECT g,
  (ARRAY['Maya Chen','Jordan Reyes','Tasha Brooks','Liam Okafor','Sofia Marino','Devin Park',
         'Aaliyah Hughes','Marcus Bell','Nina Petrova','Caleb Wright','Priya Nair','Owen Foster',
         'Bianca Rossi','Andre Dubois','Hana Suzuki','Eli Greenberg','Camila Torres','Ruben Diaz'])[g],
  ((g-1)/3)+1,
  date '2019-01-01' + ((g*37) % 1500)
FROM generate_series(1,18) g;

INSERT INTO members (member_id, name, email, tier, home_studio_id, joined_at)
SELECT g,
  fn[(g % 40) + 1] || ' ' || ln[(g % 38) + 1],
  lower(fn[(g % 40) + 1]) || '.' || lower(ln[(g % 38) + 1]) || g ||
    (ARRAY['@gmail.com','@outlook.com','@icloud.com','@yahoo.com'])[(g % 4) + 1],
  (ARRAY['unlimited','class-pack','class-pack','unlimited','trial'])[(g % 5) + 1],
  (g % 6) + 1,
  date '2023-01-01' + ((g * 7) % 760)
FROM generate_series(1,600) g,
  (SELECT ARRAY['Emma','Liam','Olivia','Noah','Ava','Ethan','Sophia','Mason','Isabella','Lucas',
                'Mia','Logan','Charlotte','James','Amelia','Aiden','Harper','Elijah','Evelyn','Jacob',
                'Abigail','Michael','Emily','Daniel','Ella','Henry','Grace','Sebastian','Chloe','Jack',
                'Zoe','Owen','Lily','Leo','Nora','Caleb','Hannah','Ryan','Layla','Nathan'] AS fn,
          ARRAY['Smith','Johnson','Williams','Brown','Jones','Garcia','Miller','Davis','Rodriguez','Martinez',
                'Hernandez','Lopez','Gonzalez','Wilson','Anderson','Thomas','Taylor','Moore','Jackson','Martin',
                'Lee','Perez','Thompson','White','Harris','Sanchez','Clark','Ramirez','Lewis','Robinson',
                'Walker','Young','Allen','King','Wright','Scott','Torres','Nguyen'] AS ln) names;

-- Sessions: 6 daily slots across 6 studios for ~60 days.
INSERT INTO sessions (session_id, studio_id, instructor_id, class_type_id, scheduled_at, capacity)
SELECT r.session_id, r.studio_id,
  ((r.studio_id - 1) * 3) + 1 + (r.session_id % 3),  -- an instructor based at this studio
  ((r.session_id * 7) % 6) + 1,                       -- rotating class type
  (r.d + r.t)::timestamp,
  r.reformers
FROM (
  SELECT row_number() OVER (ORDER BY d.d, s.studio_id, sl.t) AS session_id,
         s.studio_id, s.reformers, d.d, sl.t
  FROM studios s
  CROSS JOIN (SELECT generate_series(date '2025-04-01', date '2025-05-30', interval '1 day')::date AS d) d
  CROSS JOIN (SELECT unnest(ARRAY['06:00','07:00','09:00','12:00','17:30','18:30']::time[]) AS t) sl
) r;

-- Bookings: ~85% seat fill; ~7% cancelled, ~2% waitlisted; ~88% of booked attend.
INSERT INTO bookings (booking_id, session_id, member_id, status, attended, booked_at)
SELECT row_number() OVER (), p.session_id, p.member_id,
  CASE WHEN p.r_status < 0.07 THEN 'cancelled'
       WHEN p.r_status < 0.09 THEN 'waitlisted'
       ELSE 'booked' END,
  (p.r_status >= 0.09 AND p.r_att < 0.88),
  p.scheduled_at - interval '2 days'
FROM (
  SELECT se.session_id, se.scheduled_at,
    ((se.session_id * 31 + seat * 17) % 600) + 1 AS member_id,
    random() AS r_fill, random() AS r_status, random() AS r_att
  FROM sessions se CROSS JOIN LATERAL generate_series(1, se.capacity) AS seat
) p
WHERE p.r_fill < 0.85;

-- Payments: unlimited = monthly membership; class-pack = a 10-class pack; trial = a drop-in.
INSERT INTO payments (payment_id, member_id, kind, amount, paid_at)
SELECT row_number() OVER (), member_id, kind, amount, paid_at FROM (
  SELECT member_id, 'membership' AS kind, 199.00 AS amount,
         (date '2025-04-01' + (member_id % 28))::timestamp AS paid_at
  FROM members WHERE tier = 'unlimited'
  UNION ALL
  SELECT member_id, 'membership', 199.00, (date '2025-05-01' + (member_id % 28))::timestamp
  FROM members WHERE tier = 'unlimited'
  UNION ALL
  SELECT member_id, 'class_pack', 235.00, (date '2025-04-01' + (member_id % 50))::timestamp
  FROM members WHERE tier = 'class-pack'
  UNION ALL
  SELECT member_id, 'drop_in', 38.00, (date '2025-04-15' + (member_id % 20))::timestamp
  FROM members WHERE tier = 'trial'
) x;
