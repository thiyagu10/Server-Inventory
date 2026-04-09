from flask import Flask, render_template, request, redirect, url_for, session, flash, Response
from flask_sqlalchemy import SQLAlchemy
from werkzeug.security import generate_password_hash, check_password_hash
from functools import wraps
import csv
import io
import os
from datetime import datetime

app = Flask(__name__)
app.secret_key = os.urandom(24)
app.config['SQLALCHEMY_DATABASE_URI'] = 'sqlite:///inventory.db'
app.config['SQLALCHEMY_TRACK_MODIFICATIONS'] = False

db = SQLAlchemy(app)

# ── Models ──────────────────────────────────────────────────────────────────

class User(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    username = db.Column(db.String(80), unique=True, nullable=False)
    password_hash = db.Column(db.String(200), nullable=False)
    is_admin = db.Column(db.Boolean, default=False)

class Server(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    hostname = db.Column(db.String(100), nullable=False)
    ip_address = db.Column(db.String(45), nullable=False)
    os_name = db.Column(db.String(100))
    os_version = db.Column(db.String(50))
    cpu = db.Column(db.String(100))
    ram_gb = db.Column(db.Integer)
    disk_tb = db.Column(db.Float)
    location = db.Column(db.String(100))
    rack = db.Column(db.String(50))
    status = db.Column(db.String(20), default='online')
    owner = db.Column(db.String(100))
    team = db.Column(db.String(100))
    created_at = db.Column(db.DateTime, default=datetime.utcnow)
    updated_at = db.Column(db.DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)

# ── Auth helpers ─────────────────────────────────────────────────────────────

def login_required(f):
    @wraps(f)
    def decorated(*args, **kwargs):
        if 'user_id' not in session:
            return redirect(url_for('login'))
        return f(*args, **kwargs)
    return decorated

# ── Auth routes ──────────────────────────────────────────────────────────────

@app.route('/login', methods=['GET', 'POST'])
def login():
    if request.method == 'POST':
        user = User.query.filter_by(username=request.form['username']).first()
        if user and check_password_hash(user.password_hash, request.form['password']):
            session['user_id'] = user.id
            session['username'] = user.username
            session['is_admin'] = user.is_admin
            return redirect(url_for('index'))
        flash('Invalid credentials', 'error')
    return render_template('login.html')

@app.route('/logout')
def logout():
    session.clear()
    return redirect(url_for('login'))

# ── Server routes ─────────────────────────────────────────────────────────────

@app.route('/')
@login_required
def index():
    q = request.args.get('q', '').strip()
    status_filter = request.args.get('status', '')
    team_filter = request.args.get('team', '')

    query = Server.query
    if q:
        like = f'%{q}%'
        query = query.filter(
            db.or_(
                Server.hostname.ilike(like),
                Server.ip_address.ilike(like),
                Server.owner.ilike(like),
                Server.location.ilike(like),
                Server.team.ilike(like),
            )
        )
    if status_filter:
        query = query.filter_by(status=status_filter)
    if team_filter:
        query = query.filter_by(team=team_filter)

    servers = query.order_by(Server.hostname).all()
    teams = [r[0] for r in db.session.query(Server.team).distinct() if r[0]]
    total = Server.query.count()
    online = Server.query.filter_by(status='online').count()
    offline = Server.query.filter_by(status='offline').count()
    maintenance = Server.query.filter_by(status='maintenance').count()

    return render_template('index.html',
        servers=servers, q=q,
        status_filter=status_filter, team_filter=team_filter,
        teams=teams, total=total, online=online,
        offline=offline, maintenance=maintenance)

@app.route('/server/new', methods=['GET', 'POST'])
@login_required
def new_server():
    if request.method == 'POST':
        s = Server(
            hostname=request.form['hostname'],
            ip_address=request.form['ip_address'],
            os_name=request.form.get('os_name'),
            os_version=request.form.get('os_version'),
            cpu=request.form.get('cpu'),
            ram_gb=request.form.get('ram_gb') or None,
            disk_tb=request.form.get('disk_tb') or None,
            location=request.form.get('location'),
            rack=request.form.get('rack'),
            status=request.form.get('status', 'online'),
            owner=request.form.get('owner'),
            team=request.form.get('team'),
        )
        db.session.add(s)
        db.session.commit()
        flash(f'Server {s.hostname} added.', 'success')
        return redirect(url_for('index'))
    return render_template('server_form.html', server=None, action='Add')

@app.route('/server/<int:id>/edit', methods=['GET', 'POST'])
@login_required
def edit_server(id):
    s = Server.query.get_or_404(id)
    if request.method == 'POST':
        s.hostname = request.form['hostname']
        s.ip_address = request.form['ip_address']
        s.os_name = request.form.get('os_name')
        s.os_version = request.form.get('os_version')
        s.cpu = request.form.get('cpu')
        s.ram_gb = request.form.get('ram_gb') or None
        s.disk_tb = request.form.get('disk_tb') or None
        s.location = request.form.get('location')
        s.rack = request.form.get('rack')
        s.status = request.form.get('status', 'online')
        s.owner = request.form.get('owner')
        s.team = request.form.get('team')
        s.updated_at = datetime.utcnow()
        db.session.commit()
        flash(f'Server {s.hostname} updated.', 'success')
        return redirect(url_for('index'))
    return render_template('server_form.html', server=s, action='Edit')

@app.route('/server/<int:id>/delete', methods=['POST'])
@login_required
def delete_server(id):
    s = Server.query.get_or_404(id)
    name = s.hostname
    db.session.delete(s)
    db.session.commit()
    flash(f'Server {name} deleted.', 'success')
    return redirect(url_for('index'))

@app.route('/export')
@login_required
def export_csv():
    servers = Server.query.order_by(Server.hostname).all()
    buf = io.StringIO()
    w = csv.writer(buf)
    w.writerow(['Hostname','IP','OS','Version','CPU','RAM (GB)','Disk (TB)',
                'Location','Rack','Status','Owner','Team','Created','Updated'])
    for s in servers:
        w.writerow([s.hostname, s.ip_address, s.os_name, s.os_version,
                    s.cpu, s.ram_gb, s.disk_tb, s.location, s.rack,
                    s.status, s.owner, s.team,
                    s.created_at.strftime('%Y-%m-%d') if s.created_at else '',
                    s.updated_at.strftime('%Y-%m-%d') if s.updated_at else ''])
    buf.seek(0)
    return Response(buf, mimetype='text/csv',
        headers={'Content-Disposition': 'attachment; filename=servers.csv'})

# ── Init ─────────────────────────────────────────────────────────────────────

def init_db():
    with app.app_context():
        db.create_all()
        if not User.query.filter_by(username='admin').first():
            admin = User(
                username='admin',
                password_hash=generate_password_hash('admin123'),
                is_admin=True
            )
            db.session.add(admin)
            db.session.commit()

if __name__ == '__main__':
    init_db()
    app.run(debug=True)
