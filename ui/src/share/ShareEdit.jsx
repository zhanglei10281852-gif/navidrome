import {
  DateTimeInput,
  BooleanInput,
  Edit,
  FunctionField,
  NumberField,
  NumberInput,
  SimpleForm,
  TextInput,
  useDataProvider,
  useNotify,
  useRefresh,
  useTranslate,
} from 'react-admin'
import { sharePlayerUrl } from '../utils'
import { Button, Link } from '@material-ui/core'
import { DateField } from '../common'
import config from '../config'

const RotateButton = ({ record }) => {
  const dataProvider = useDataProvider()
  const notify = useNotify()
  const refresh = useRefresh()
  const translate = useTranslate()

  const rotate = () => {
    if (!window.confirm(translate('resources.share.actions.rotateConfirm'))) {
      return
    }
    dataProvider
      .rotateShare(record?.id)
      .then(({ data }) => {
        notify(
          translate('resources.share.notifications.rotated', {
            epoch: data?.epoch,
          }),
          'info',
        )
        refresh()
      })
      .catch((err) =>
        notify(
          translate('ra.page.error') + ': ' + err.message,
          'warning',
        ),
      )
  }

  return (
    <Button
      variant="contained"
      color="secondary"
      onClick={rotate}
      style={{ marginTop: '1em' }}
    >
      {translate('resources.share.actions.rotate')}
    </Button>
  )
}

const UsageField = ({ label, usedSource, totalSource }) => (
  <FunctionField
    label={label}
    render={(record) => {
      const used = record?.[usedSource] ?? 0
      const total = record?.[totalSource]
      return `${used} / ${total > 0 ? total : '∞'}`
    }}
  />
)

export const ShareEdit = (props) => {
  const { id } = props
  const url = sharePlayerUrl(id)
  return (
    <Edit {...props}>
      <SimpleForm>
        <Link source="URL" href={url} target="_blank" rel="noopener noreferrer">
          {url}
        </Link>
        <TextInput source="description" />
        {config.enableDownloads && <BooleanInput source="downloadable" />}
        <DateTimeInput source="expiresAt" />
        <NumberInput
          source="maxConcurrentPlays"
          min={0}
          step={1}
          helperText="0 = unlimited"
        />
        {config.enableDownloads && (
          <NumberInput
            source="maxDownloads"
            min={0}
            step={1}
            helperText="0 = unlimited"
          />
        )}
        <RotateButton />
        <TextInput source="contents" disabled />
        <TextInput source="format" disabled />
        <TextInput source="maxBitRate" disabled />
        <TextInput source="username" disabled />
        <NumberField source="visitCount" disabled />
        <UsageField
          label="resources.share.fields.activePlays"
          usedSource="activePlays"
          totalSource="maxConcurrentPlays"
        />
        {config.enableDownloads && (
          <UsageField
            label="resources.share.fields.downloadCount"
            usedSource="downloadCount"
            totalSource="maxDownloads"
          />
        )}
        <NumberField source="epoch" disabled />
        <DateField source="lastVisitedAt" disabled showTime />
        <DateField source="createdAt" disabled showTime />
      </SimpleForm>
    </Edit>
  )
}
