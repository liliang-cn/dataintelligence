-- EXAMPLE integration #2: "Reformd" — a boutique reformer-pilates studio chain
-- (Solidcore-style: 50-minute high-intensity, low-impact classes on a reformer).
-- A second domain proving DataIntelligence is neutral: the platform code knows
-- nothing about fitness — this is just data + a semantic model.

DROP TABLE IF EXISTS bookings, payments, sessions, members, instructors, class_types, studios CASCADE;

CREATE TABLE studios (
  studio_id  int PRIMARY KEY,
  name       text NOT NULL,
  city       text NOT NULL,
  region     text NOT NULL,   -- row-level-security key (a regional manager sees their region)
  reformers  int  NOT NULL,   -- machines = per-class capacity
  opened_at  date NOT NULL
);

CREATE TABLE class_types (
  class_type_id int PRIMARY KEY,
  name          text NOT NULL,
  intensity     text NOT NULL, -- beginner | signature | advanced
  duration_min  int  NOT NULL
);

CREATE TABLE instructors (
  instructor_id int PRIMARY KEY,
  name          text NOT NULL,
  studio_id     int  NOT NULL REFERENCES studios(studio_id),
  hired_at      date NOT NULL
);

CREATE TABLE members (
  member_id      int PRIMARY KEY,
  name           text NOT NULL,
  email          text NOT NULL,   -- PII (masking demo)
  tier           text NOT NULL,   -- trial | class-pack | unlimited
  home_studio_id int  NOT NULL REFERENCES studios(studio_id),
  joined_at      date NOT NULL
);

CREATE TABLE sessions (
  session_id    int PRIMARY KEY,
  studio_id     int       NOT NULL REFERENCES studios(studio_id),
  instructor_id int       NOT NULL REFERENCES instructors(instructor_id),
  class_type_id int       NOT NULL REFERENCES class_types(class_type_id),
  scheduled_at  timestamp NOT NULL,
  capacity      int       NOT NULL
);

CREATE TABLE bookings (
  booking_id bigint PRIMARY KEY,
  session_id int       NOT NULL REFERENCES sessions(session_id),
  member_id  int       NOT NULL REFERENCES members(member_id),
  status     text      NOT NULL,  -- booked | cancelled | waitlisted
  attended   boolean   NOT NULL,  -- checked in at the front desk
  booked_at  timestamp NOT NULL
);

CREATE TABLE payments (
  payment_id bigint PRIMARY KEY,
  member_id  int       NOT NULL REFERENCES members(member_id),
  kind       text      NOT NULL,  -- membership | class_pack | drop_in
  amount     numeric   NOT NULL,
  paid_at    timestamp NOT NULL
);
