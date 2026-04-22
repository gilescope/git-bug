import AppBar from '@mui/material/AppBar';
import Toolbar from '@mui/material/Toolbar';
import { alpha } from '@mui/material/styles';
import makeStyles from '@mui/styles/makeStyles';
import { Link, useLocation } from 'react-router';

import CurrentIdentity from '../Identity/CurrentIdentity';
import CurrentRepository from '../Identity/CurrentRepository';
import { LightSwitch } from '../Themer';

import GlobalSearch from './GlobalSearch';
import RepoPicker from './RepoPicker';
import SyncButton from './SyncButton';

const useStyles = makeStyles((theme) => ({
  offset: {
    ...theme.mixins.toolbar,
  },
  filler: {
    flexGrow: 1,
  },
  appBar: {
    backgroundColor: theme.palette.primary.dark,
    color: theme.palette.primary.contrastText,
  },
  appTitle: {
    ...theme.typography.h6,
    color: theme.palette.primary.contrastText,
    textDecoration: 'none',
    display: 'flex',
    alignItems: 'center',
  },
  lightSwitch: {
    marginRight: theme.spacing(2),
    color: alpha(theme.palette.primary.contrastText, 0.5),
  },
  logo: {
    height: '42px',
    marginRight: theme.spacing(2),
  },
  // Nav link to /r/<repo>/projects. Only rendered when we're already
  // inside a repo route — projects are owner-scoped and there's no
  // sensible "global projects" view.
  navLink: {
    color: alpha(theme.palette.primary.contrastText, 0.85),
    textDecoration: 'none',
    padding: theme.spacing(0.5, 1),
    marginRight: theme.spacing(1),
    fontSize: '0.9rem',
    '&:hover': {
      color: theme.palette.primary.contrastText,
      background: alpha(theme.palette.primary.contrastText, 0.08),
      borderRadius: 4,
    },
  },
}));

// repoFromPath extracts the :repoName segment from /r/<name>/... so the
// Header can render a Projects link without wiring up a full route
// binding.
function repoFromPath(pathname: string): string | null {
  const m = pathname.match(/^\/r\/([^/]+)/);
  if (!m) return null;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return null;
  }
}

function Header() {
  const classes = useStyles();
  const location = useLocation();
  const repoName = repoFromPath(location.pathname);

  return (
    <>
      <AppBar position="fixed" className={classes.appBar}>
        <Toolbar>
          <Link to="/" className={classes.appTitle}>
            <img src="/logo.svg" className={classes.logo} alt="git-bug logo" />
            <CurrentRepository default="git-bug" />
          </Link>
          <RepoPicker />
          {repoName && (
            <Link
              to={`/r/${encodeURIComponent(repoName)}/projects`}
              className={classes.navLink}
            >
              Projects
            </Link>
          )}
          <GlobalSearch />
          <div className={classes.filler} />
          <SyncButton />
          <LightSwitch className={classes.lightSwitch} />
          <CurrentIdentity />
        </Toolbar>
      </AppBar>
      <div className={classes.offset}></div>
    </>
  );
}

export default Header;
